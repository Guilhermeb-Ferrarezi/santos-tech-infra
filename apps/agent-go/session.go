package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

// turnEvent é o evento normalizado enviado ao cliente (WS) durante um turno.
type turnEvent struct {
	Type    string `json:"type"` // init|delta|tool_use|tool_result|result|error|done
	Text    string `json:"text,omitempty"`
	Name    string `json:"name,omitempty"`
	Input   any    `json:"input,omitempty"`
	Data    any    `json:"data,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

var errBusy = appErr(http.StatusConflict, "BUSY", "Já há um turno em andamento nesta conversa")

// SessionManager orquestra os processos `claude` (um por turno).
type SessionManager struct {
	s    *Server
	mu   sync.Mutex
	runs map[string]*exec.Cmd               // convID -> processo do turno atual (para interrupção)
	subs map[string]map[chan turnEvent]bool // convID -> WSs conectados (assinantes dos eventos)
}

func newSessionManager(s *Server) *SessionManager {
	return &SessionManager{s: s, runs: map[string]*exec.Cmd{}, subs: map[string]map[chan turnEvent]bool{}}
}

// Subscribe registra um assinante (WS) para os eventos de uma conversa. O turno roda
// independente do WS — assinar só serve para receber o stream ao vivo enquanto conectado.
func (m *SessionManager) Subscribe(convID string) (<-chan turnEvent, func()) {
	ch := make(chan turnEvent, 256)
	m.mu.Lock()
	if m.subs[convID] == nil {
		m.subs[convID] = map[chan turnEvent]bool{}
	}
	m.subs[convID][ch] = true
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.subs[convID], ch)
		if len(m.subs[convID]) == 0 {
			delete(m.subs, convID)
		}
		m.mu.Unlock()
		close(ch)
	}
}

func (m *SessionManager) hasSubs(convID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.subs[convID]) > 0
}

// dispatch envia um evento a todos os assinantes (não-bloqueante: descarta se o buffer encher).
func (m *SessionManager) dispatch(convID string, ev turnEvent) {
	m.mu.Lock()
	for ch := range m.subs[convID] {
		select {
		case ch <- ev:
		default:
		}
	}
	m.mu.Unlock()
}

// Interrupt encerra o turno em andamento de uma conversa (SIGTERM — o CLI salva a sessão).
func (m *SessionManager) Interrupt(convID string) bool {
	m.mu.Lock()
	cmd := m.runs[convID]
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return false
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	return true
}

// InterruptAll encerra TODOS os turnos em andamento (kill switch). Retorna quantos.
func (m *SessionManager) InterruptAll() int {
	m.mu.Lock()
	cmds := make([]*exec.Cmd, 0, len(m.runs))
	for _, c := range m.runs {
		cmds = append(cmds, c)
	}
	m.mu.Unlock()
	n := 0
	for _, c := range cmds {
		if c.Process != nil {
			_ = c.Process.Signal(syscall.SIGTERM)
			n++
		}
	}
	return n
}

// deriveTitle gera um título curto a partir do 1º prompt (primeira linha, até 48 chars).
func deriveTitle(prompt string) string {
	line := strings.TrimSpace(prompt)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	line = strings.TrimLeft(line, "/#-* ")
	if r := []rune(line); len(r) > 48 {
		line = strings.TrimSpace(string(r[:48])) + "…"
	}
	return line
}

// RunTurn executa um turno completo, DESACOPLADO do WebSocket: roda em background
// (sobrevive ao app fechar / WS cair), transmite via dispatch para os assinantes e
// persiste as mensagens. Ao terminar, envia push se ninguém estiver conectado.
func (m *SessionManager) RunTurn(conv *Conversation, prompt string) {
	ctx := context.Background()
	if ok, _ := m.s.acquireTurn(ctx, conv.ID); !ok {
		m.dispatch(conv.ID, turnEvent{Type: "busy"})
		return
	}
	defer m.s.releaseTurn(ctx, conv.ID)

	emit := func(ev turnEvent) { m.dispatch(conv.ID, ev) }

	_ = m.s.setConversationStatus(ctx, conv.ID, StatusRunning)
	m.s.setState(ctx, conv.ID, StatusRunning)
	_ = m.s.insertMessage(ctx, &Message{
		ConversationID: conv.ID, Role: "user", Kind: "text",
		Content: map[string]any{"text": prompt},
	})

	// Auto-título no 1º turno, se ainda não tiver.
	if (conv.Title == nil || *conv.Title == "") && !conv.SessionStarted {
		if title := deriveTitle(prompt); title != "" {
			_ = m.s.setTitleIfEmpty(ctx, conv.ID, title)
			conv.Title = &title
			m.dispatch(conv.ID, turnEvent{Type: "title", Text: title})
		}
	}

	// Seed pendente (deixado por /compact): vira contexto da nova sessão.
	effective := prompt
	if seed, _ := m.s.rdb.GetDel(ctx, "claude:seed:"+conv.ID).Result(); seed != "" {
		effective = seed + "\n\n---\n\n" + prompt
	}

	err := m.exec(ctx, conv, effective, emit)

	status := StatusIdle
	if err != nil {
		status = StatusError
		emit(turnEvent{Type: "error", Code: "TURN_FAILED", Message: err.Error()})
	}
	_ = m.s.setConversationStatus(ctx, conv.ID, status)
	m.s.setState(ctx, conv.ID, status)
	if err == nil && !conv.SessionStarted {
		_ = m.s.markSessionStarted(ctx, conv.ID)
		conv.SessionStarted = true
	}
	emit(turnEvent{Type: "done"})

	// Push só se ninguém estiver assistindo (app em background / fechado).
	if !m.hasSubs(conv.ID) {
		m.s.notifyTurnDone(ctx, conv, status)
	}
}

// RunTurnCollect roda um turno e coleta o texto do assistente (usado pelo /compact).
func (m *SessionManager) RunTurnCollect(conv *Conversation, prompt string) (string, error) {
	events, unsub := m.Subscribe(conv.ID)
	defer unsub()
	go m.RunTurn(conv, prompt)
	var b strings.Builder
	for ev := range events {
		switch ev.Type {
		case "delta":
			b.WriteString(ev.Text)
		case "busy":
			return "", errBusy
		case "error":
			return b.String(), fmt.Errorf("%s", ev.Message)
		case "done":
			return b.String(), nil
		}
	}
	return b.String(), nil
}

// claudeArgs monta os argumentos do CLI para um turno.
func (m *SessionManager) claudeArgs(conv *Conversation) []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--dangerously-skip-permissions",
		"--model", conv.Model,
		"--add-dir", conv.Workdir,
	}
	if mcp := m.s.mcpConfigPath(conv); fileExists(mcp) {
		args = append(args, "--mcp-config", mcp, "--strict-mcp-config")
	}
	if conv.SessionStarted {
		args = append(args, "--resume", conv.SessionID)
	} else {
		args = append(args, "--session-id", conv.SessionID)
	}
	return args
}

// claudeEnv monta o ambiente do processo: token OAuth + tokens de infra para o agente.
func (m *SessionManager) claudeEnv(ctx context.Context) []string {
	env := os.Environ()
	if tok, err := m.s.oauthToken(ctx); err == nil && tok != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+tok)
	}
	if t := m.s.cfg.GithubToken; t != "" {
		env = append(env, "GITHUB_TOKEN="+t, "GITHUB_PERSONAL_ACCESS_TOKEN="+t)
	}
	if t := m.s.cfg.EasypanelToken; t != "" {
		env = append(env, "EASYPANEL_TOKEN="+t, "EASYPANEL_URL="+m.s.cfg.EasypanelURL)
	}
	if t := m.s.cfg.CloudflareToken; t != "" {
		env = append(env, "CLOUDFLARE_API_TOKEN="+t)
	}
	return env
}

func (m *SessionManager) exec(ctx context.Context, conv *Conversation, prompt string, emit func(turnEvent)) error {
	cmd := exec.CommandContext(ctx, m.s.cfg.ClaudeBin, m.claudeArgs(conv)...)
	cmd.Dir = conv.Workdir
	cmd.Env = m.claudeEnv(ctx)
	cmd.Stdin = strings.NewReader(prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr // logs do CLI vão pro stderr do serviço

	if err := cmd.Start(); err != nil {
		return err
	}
	m.mu.Lock()
	m.runs[conv.ID] = cmd
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.runs, conv.ID)
		m.mu.Unlock()
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal(line, &ev); err != nil {
			slog.Warn("linha stream-json inválida", "line", string(line))
			continue
		}
		m.handleEvent(ctx, conv, ev, emit)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("claude saiu com erro: %w", err)
	}
	return sc.Err()
}

// handleEvent normaliza um evento do stream-json: transmite (emit) e persiste.
func (m *SessionManager) handleEvent(ctx context.Context, conv *Conversation, ev map[string]any, emit func(turnEvent)) {
	switch ev["type"] {
	case "system":
		emit(turnEvent{Type: "init", Data: ev})
		_ = m.s.insertMessage(ctx, &Message{ConversationID: conv.ID, Role: "system", Kind: "text", Content: ev})

	case "stream_event":
		// Deltas de texto ao vivo (não persistidos; o texto final vem no "assistant").
		if t := deltaText(ev); t != "" {
			emit(turnEvent{Type: "delta", Text: t})
		}

	case "assistant":
		for _, block := range messageContent(ev) {
			switch block["type"] {
			case "text":
				if t, _ := block["text"].(string); t != "" {
					_ = m.s.insertMessage(ctx, &Message{ConversationID: conv.ID, Role: "assistant", Kind: "text", Content: block})
				}
			case "tool_use":
				name, _ := block["name"].(string)
				emit(turnEvent{Type: "tool_use", Name: name, Input: block["input"]})
				_ = m.s.insertMessage(ctx, &Message{ConversationID: conv.ID, Role: "assistant", Kind: "tool_use", Content: block})
			}
		}

	case "user":
		for _, block := range messageContent(ev) {
			if block["type"] == "tool_result" {
				emit(turnEvent{Type: "tool_result", Data: block})
				_ = m.s.insertMessage(ctx, &Message{ConversationID: conv.ID, Role: "tool", Kind: "tool_result", Content: block})
			}
		}

	case "result":
		usage, _ := ev["usage"].(map[string]any)
		emit(turnEvent{Type: "result", Data: ev})
		_ = m.s.insertMessage(ctx, &Message{ConversationID: conv.ID, Role: "system", Kind: "result", Content: ev, Usage: usage})
	}
}

// ── helpers de navegação no JSON ─────────────────────────────────────────────

func messageContent(ev map[string]any) []map[string]any {
	msg, ok := ev["message"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := msg["content"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, b := range raw {
		if bm, ok := b.(map[string]any); ok {
			out = append(out, bm)
		}
	}
	return out
}

// deltaText extrai o texto de um stream_event de content_block_delta.
func deltaText(ev map[string]any) string {
	e, ok := ev["event"].(map[string]any)
	if !ok {
		return ""
	}
	if e["type"] != "content_block_delta" {
		return ""
	}
	d, ok := e["delta"].(map[string]any)
	if !ok {
		return ""
	}
	if d["type"] == "text_delta" {
		t, _ := d["text"].(string)
		return t
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
