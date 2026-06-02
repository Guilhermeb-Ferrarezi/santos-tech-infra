package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// claudeAuth gerencia o fluxo OAuth da assinatura (claude setup-token), que é
// interativo: abre uma URL e espera um código de volta. Mantemos o PTY vivo entre
// /auth/login e /auth/callback.
type claudeAuth struct {
	cfg  Config
	mu   sync.Mutex
	pend map[string]*pendingAuth
}

type pendingAuth struct {
	ptmx    *os.File
	cmd     *exec.Cmd
	buf     *safeBuffer
	created time.Time
}

func newClaudeAuth(cfg Config) *claudeAuth {
	return &claudeAuth{cfg: cfg, pend: map[string]*pendingAuth{}}
}

var (
	urlRe   = regexp.MustCompile(`https?://[^\s'"]+`)
	tokenRe = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)
)

// POST /claude/auth/login
// Corpo opcional {token}: atalho que grava um token gerado fora (claude setup-token
// na máquina do usuário). Sem token: inicia o fluxo PTY e devolve {state, authUrl}.
func (s *Server) handleClaudeAuthLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	_ = decodeJSON(r, &body)

	if t := strings.TrimSpace(body.Token); t != "" {
		if err := s.storeOAuth(r.Context(), t); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "logged_in"})
		return
	}

	state, authURL, err := s.auth.start(s.cfg.ClaudeBin)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "OAUTH_START_FAILED", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": state, "authUrl": authURL})
}

// POST /claude/auth/callback {state, code}
func (s *Server) handleClaudeAuthCallback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State string `json:"state"`
		Code  string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil || body.State == "" || body.Code == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "Campos 'state' e 'code' obrigatórios"))
		return
	}
	token, err := s.auth.complete(body.State, body.Code)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "OAUTH_COMPLETE_FAILED", err.Error()))
		return
	}
	if err := s.storeOAuth(r.Context(), token); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "logged_in"})
}

// POST /claude/auth/logout
func (s *Server) handleClaudeAuthLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.clearOAuthToken(r.Context()); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "logged_out"})
}

// GET /claude/auth/status
func (s *Server) handleClaudeAuthStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.oauthStatus(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status})
}

// storeOAuth cifra e persiste o token OAuth.
func (s *Server) storeOAuth(ctx context.Context, token string) error {
	enc, err := encrypt(s.cfg.EncryptionKey, token)
	if err != nil {
		return err
	}
	return s.saveOAuthToken(ctx, enc)
}

// ── PTY: setup-token ─────────────────────────────────────────────────────────

// start dispara `claude setup-token` num PTY e espera a URL de OAuth aparecer.
func (a *claudeAuth) start(claudeBin string) (state, authURL string, err error) {
	cmd := exec.Command(claudeBin, "setup-token")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return "", "", err
	}
	// PTY bem largo: o setup-token imprime uma URL longa que, a 80 colunas, seria
	// quebrada em várias linhas e truncaria a captura.
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 200, Cols: 1000})
	buf := &safeBuffer{}
	go func() { _, _ = copyInto(buf, ptmx) }()

	// Espera a URL aparecer (até 20s).
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if m := urlRe.FindString(cleanTTY(buf.String())); m != "" {
			state = newUUID()
			a.mu.Lock()
			a.pend[state] = &pendingAuth{ptmx: ptmx, cmd: cmd, buf: buf, created: time.Now()}
			a.mu.Unlock()
			a.gc()
			return state, m, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = ptmx.Close()
	return "", "", errURLNotFound(buf.String())
}

// complete escreve o código no PTY e captura o token impresso.
func (a *claudeAuth) complete(state, code string) (string, error) {
	a.mu.Lock()
	p := a.pend[state]
	delete(a.pend, state)
	a.mu.Unlock()
	if p == nil {
		return "", errState
	}
	defer func() { _ = p.ptmx.Close() }()

	if _, err := p.ptmx.Write([]byte(strings.TrimSpace(code) + "\n")); err != nil {
		return "", err
	}

	// Aguarda o token aparecer / o processo terminar (até 30s).
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m := tokenRe.FindString(cleanTTY(p.buf.String())); m != "" {
			_, _ = p.cmd.Process.Wait()
			return m, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Fallback: última linha não vazia.
	if t := lastNonEmptyLine(cleanTTY(p.buf.String())); t != "" {
		return t, nil
	}
	return "", errTokenNotFound(p.buf.String())
}

// ansiRe casa sequências de escape ANSI (cores, cursor) que o TUI pode emitir.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// cleanTTY remove escapes ANSI e CRs da saída do PTY para casar URL/token de forma estável.
func cleanTTY(s string) string {
	return strings.ReplaceAll(ansiRe.ReplaceAllString(s, ""), "\r", "")
}

// gc remove fluxos pendentes com mais de 5min.
func (a *claudeAuth) gc() {
	for state, p := range a.pend {
		if time.Since(p.created) > 5*time.Minute {
			_ = p.cmd.Process.Kill()
			_ = p.ptmx.Close()
			delete(a.pend, state)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// safeBuffer é um bytes.Buffer protegido por mutex (escrito pela goroutine do PTY).
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func copyInto(dst *safeBuffer, src io.Reader) (int64, error) {
	return io.Copy(dst, src)
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

func errURLNotFound(out string) error {
	return appErr(http.StatusBadGateway, "OAUTH_URL_NOT_FOUND", "URL de OAuth não encontrada na saída do CLI: "+truncate(out, 300))
}
func errTokenNotFound(out string) error {
	return appErr(http.StatusBadGateway, "OAUTH_TOKEN_NOT_FOUND", "Token não encontrado na saída do CLI: "+truncate(out, 300))
}

var errState = appErr(http.StatusBadRequest, "INVALID_STATE", "Fluxo de login não encontrado ou expirado")

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
