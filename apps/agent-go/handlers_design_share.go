package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"html"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	agentdb "github.com/santos-tech/agent/db"
)

// ── Telas do projeto ────────────────────────────────────────────────────────

// designScreen é uma tela do projeto, como aparece no seletor do canvas.
type designScreen struct {
	Path      string `json:"path"`      // relativo ao workdir: "telas/login.html"
	Title     string `json:"title"`     // <title> do HTML, ou o nome do arquivo
	UpdatedAt string `json:"updatedAt"` // mtime (RFC3339) — "editada há 2 min" no seletor
}

// maxDesignScreens limita a listagem: o seletor é uma lista, não um explorador.
const maxDesignScreens = 100

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// screenTitle lê o <title> do começo do arquivo; sem ele, usa o nome do arquivo.
func screenTitle(full string) string {
	name := strings.TrimSuffix(filepath.Base(full), filepath.Ext(full))
	f, err := os.Open(full)
	if err != nil {
		return name
	}
	defer f.Close()
	head, _ := io.ReadAll(io.LimitReader(f, 8<<10))
	if m := titleRe.FindSubmatch(head); m != nil {
		if t := strings.Join(strings.Fields(html.UnescapeString(string(m[1]))), " "); t != "" {
			return t
		}
	}
	return name
}

// listDesignScreens lista telas/*.html — a principal (index) primeiro, o resto por
// nome de arquivo.
func listDesignScreens(workdir string) ([]designScreen, error) {
	entries, err := os.ReadDir(filepath.Join(workdir, "telas"))
	if errors.Is(err, os.ErrNotExist) {
		return []designScreen{}, nil
	}
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.EqualFold(filepath.Ext(n), ".html") && !strings.HasPrefix(n, ".") {
			names = append(names, n)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		ii, jj := names[i] == designScreenSlug+".html", names[j] == designScreenSlug+".html"
		if ii != jj {
			return ii
		}
		return names[i] < names[j]
	})
	if len(names) > maxDesignScreens {
		names = names[:maxDesignScreens]
	}
	out := make([]designScreen, 0, len(names))
	for _, n := range names {
		full := filepath.Join(workdir, "telas", n)
		sc := designScreen{Path: "telas/" + n, Title: screenTitle(full)}
		if info, err := os.Stat(full); err == nil {
			sc.UpdatedAt = info.ModTime().UTC().Format(time.RFC3339)
		}
		out = append(out, sc)
	}
	return out, nil
}

// ownedDesign devolve o projeto de design do usuário autenticado, ou escreve 404.
func (s *Server) ownedDesign(w http.ResponseWriter, r *http.Request) *Conversation {
	conv, err := s.conversationByID(r.Context(), r.PathValue("id"), userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return nil
	}
	if conv == nil || conv.Kind != designKind {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Projeto não encontrado"))
		return nil
	}
	return conv
}

// handleDesignFiles: GET /claude/designs/{id}/files → {"screens":[{path,title}]}.
func (s *Server) handleDesignFiles(w http.ResponseWriter, r *http.Request) {
	conv := s.ownedDesign(w, r)
	if conv == nil {
		return
	}
	screens, err := listDesignScreens(conv.Workdir)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"screens": screens})
}

// ── Compartilhar ────────────────────────────────────────────────────────────

// shareWorkdirResolver existe para o teste (sem Postgres): token → workdir.
type shareWorkdirResolver func(ctx context.Context, token string) (string, error)

// newShareToken gera o segredo do link público: 24 bytes aleatórios (192 bits),
// base64url sem padding — 32 caracteres.
func newShareToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// shareTokenRe valida o formato antes de ir ao banco: rota pública, lixo não consulta.
var shareTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)

type shareStatus struct {
	Active    bool   `json:"active"`
	Token     string `json:"token,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
}

func (s *Server) activeShare(ctx context.Context, convID string) (shareStatus, error) {
	row, err := s.q.GetActiveDesignShare(ctx, uuidFromStr(convID))
	if errors.Is(err, pgx.ErrNoRows) {
		return shareStatus{}, nil
	}
	if err != nil {
		return shareStatus{}, err
	}
	return shareStatus{Active: true, Token: row.Token, CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339)}, nil
}

// handleDesignShareGet: GET /claude/designs/{id}/share → estado do link.
func (s *Server) handleDesignShareGet(w http.ResponseWriter, r *http.Request) {
	conv := s.ownedDesign(w, r)
	if conv == nil {
		return
	}
	st, err := s.activeShare(r.Context(), conv.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleDesignShareCreate: POST /claude/designs/{id}/share → cria o link, ou devolve o
// que já está ativo (um por projeto; o índice único parcial garante isso no banco).
func (s *Server) handleDesignShareCreate(w http.ResponseWriter, r *http.Request) {
	conv := s.ownedDesign(w, r)
	if conv == nil {
		return
	}
	ctx := r.Context()
	if st, err := s.activeShare(ctx, conv.ID); err != nil {
		writeErr(w, err)
		return
	} else if st.Active {
		writeJSON(w, http.StatusOK, st)
		return
	}
	tok, err := newShareToken()
	if err != nil {
		writeErr(w, err)
		return
	}
	insErr := s.q.InsertDesignShare(ctx, agentdb.InsertDesignShareParams{
		Token: tok, Column2: uuidFromStr(conv.ID), CreatedBy: userIDFrom(r),
	})
	// Corrida com outro clique: o índice único recusa o 2º; devolve o que venceu.
	st, err := s.activeShare(ctx, conv.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !st.Active {
		if insErr != nil {
			writeErr(w, insErr)
			return
		}
		writeErr(w, appErr(http.StatusInternalServerError, "SHARE_FAILED", "Não foi possível criar o link"))
		return
	}
	writeJSON(w, http.StatusCreated, st)
}

// handleDesignShareRevoke: DELETE /claude/designs/{id}/share → o link para de
// responder na hora (o público não tem cache).
func (s *Server) handleDesignShareRevoke(w http.ResponseWriter, r *http.Request) {
	conv := s.ownedDesign(w, r)
	if conv == nil {
		return
	}
	if _, err := s.q.RevokeDesignShares(r.Context(), uuidFromStr(conv.ID)); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resolveShareWorkdir(ctx context.Context, token string) (string, error) {
	wd, err := s.q.GetSharedDesignWorkdir(ctx, token)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", appErr(http.StatusNotFound, "NOT_FOUND", "Link inválido ou revogado")
	}
	return wd, err
}

// handleDesignShared: GET /claude/share/{token}/{path...} — PÚBLICO, sem login.
//
// Serve só telas/ e assets/ (nunca CLAUDE.md, design.json ou .git), com a mesma CSP
// do preview mais frame-ancestors 'none'. O `sandbox` da CSP deixa a página numa
// origem opaca mesmo aberta direto numa aba: o HTML gerado por modelo não enxerga
// cookie nem storage de api.santos-tech.com.
func (s *Server) handleDesignShared(w http.ResponseWriter, r *http.Request) {
	notFound := appErr(http.StatusNotFound, "NOT_FOUND", "Link inválido ou revogado")
	tok := r.PathValue("token")
	if !shareTokenRe.MatchString(tok) {
		writeErr(w, notFound)
		return
	}
	rel := r.PathValue("path")
	if rel == "" {
		rel = designScreenRel()
	}
	// Normaliza ANTES de checar o prefixo: "telas/../design.json" não é telas/.
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	if !strings.HasPrefix(rel, "telas/") && !strings.HasPrefix(rel, "assets/") {
		writeErr(w, notFound)
		return
	}
	lookup := s.shareWorkdirFor
	if lookup == nil {
		lookup = s.resolveShareWorkdir
	}
	workdir, err := lookup(r.Context(), tok)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	serveDesignFile(w, rel, workdir, designCSPWithAncestors("'none'"), false)
}
