package main

import (
	_ "embed"
	"net/http"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

// Canvas do Claude Design, no modelo do Claude Design da Anthropic: uma casca fixa
// (canvas_shell.html) carregada UMA vez num iframe, e o painel injeta o HTML de cada
// tela por postMessage. Trocar de tela, recarregar depois de um turno e o rascunho ao
// vivo do Write não navegam o iframe — nada pisca e a rolagem fica.
//
// O token do preview vai no CAMINHO (/c/{token}/...), não na query: a tela injetada
// usa <base href> apontando pra cá, e URL relativa (assets/logo.svg) perderia a query.

//go:embed canvas_shell.html
var canvasShellHTML []byte

// maxSourceBytes: teto do HTML devolvido por /source (uma tela, não um bundle).
const maxSourceBytes = 2 << 20

// canvasRel normaliza o caminho pedido e só aceita telas/ e assets/ — nunca CLAUDE.md,
// design.json ou .git. Normaliza ANTES do prefixo: "telas/../x" não é telas/.
func canvasRel(rel string) (string, bool) {
	clean := strings.TrimPrefix(path.Clean("/"+rel), "/")
	if !strings.HasPrefix(clean, "telas/") && !strings.HasPrefix(clean, "assets/") {
		return "", false
	}
	return clean, true
}

// handleDesignCanvas: GET /claude/designs/{id}/c/{token}/{path...} — fora do authGuard
// (vive num iframe de origem opaca, sem cookie); autentica pelo token assinado.
// Sem path: a casca. Com path: o asset que a tela injetada referencia.
func (s *Server) handleDesignCanvas(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := verifyPreviewToken(s.cfg.JWTSecret, id, r.PathValue("token")); err != nil {
		writeErr(w, err)
		return
	}
	rel := r.PathValue("path")
	if rel == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", designCSP(s.cfg))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "private, no-store")
		_, _ = w.Write(canvasShellHTML)
		return
	}
	clean, ok := canvasRel(rel)
	if !ok {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Não encontrado"))
		return
	}
	lookup := s.designWorkdirFor
	if lookup == nil {
		lookup = s.resolveDesignWorkdir
	}
	workdir, err := lookup(r, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	serveDesignFile(w, clean, workdir, designCSP(s.cfg), false)
}

// handleDesignSource: GET /claude/designs/{id}/source?path=telas/x.html (admin) →
// {path, html}. É o que o painel injeta na casca. Só telas .html.
func (s *Server) handleDesignSource(w http.ResponseWriter, r *http.Request) {
	conv := s.ownedDesign(w, r)
	if conv == nil {
		return
	}
	notFound := appErr(http.StatusNotFound, "NOT_FOUND", "Tela não encontrada")
	rel := r.URL.Query().Get("path")
	if rel == "" {
		rel = designScreenRel()
	}
	clean, ok := canvasRel(rel)
	if !ok || !strings.HasPrefix(clean, "telas/") || !strings.EqualFold(path.Ext(clean), ".html") {
		writeErr(w, notFound)
		return
	}
	full, err := safeDesignPath(conv.Workdir, clean)
	if err != nil {
		writeErr(w, notFound)
		return
	}
	info, err := os.Stat(full)
	if err != nil || info.Size() > maxSourceBytes {
		writeErr(w, notFound)
		return
	}
	data, err := os.ReadFile(full)
	if err != nil || !utf8.Valid(data) {
		writeErr(w, notFound)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"path": clean, "html": string(data)})
}
