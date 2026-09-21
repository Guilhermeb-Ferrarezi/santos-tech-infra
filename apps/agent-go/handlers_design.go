package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// designWorkdirResolver existe para o teste: em produção é o lookup no Postgres
// (resolveDesignWorkdir); no teste, uma função que devolve um TempDir.
type designWorkdirResolver func(r *http.Request, convID string) (string, error)

// resolveDesignWorkdir devolve o workdir de um projeto de design SEM filtrar por
// usuário: esta rota roda fora do authGuard (userIDFrom seria 0), e quem autoriza
// é o token assinado — emitido só depois de handleDesignPreviewToken ter conferido
// o dono com conversationByID.
func (s *Server) resolveDesignWorkdir(r *http.Request, convID string) (string, error) {
	notFound := appErr(http.StatusNotFound, "NOT_FOUND", "Projeto não encontrado")
	conv, err := s.conversationByIDAny(r.Context(), convID)
	if err != nil {
		return "", err
	}
	if conv == nil || conv.Kind != designKind {
		return "", notFound
	}
	return conv.Workdir, nil
}

// handleDesignPreviewToken emite o token curto que o iframe carrega na URL. Aqui a
// conversa É filtrada pelo usuário autenticado — é este handler que faz a
// autorização de verdade.
func (s *Server) handleDesignPreviewToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	conv, err := s.conversationByID(r.Context(), id, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if conv == nil || conv.Kind != designKind {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Projeto não encontrado"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     previewToken(s.cfg.JWTSecret, id, previewTokenTTL),
		"expiresAt": time.Now().Add(previewTokenTTL).UTC().Format(time.RFC3339),
	})
}

// handleDesignPreview serve um arquivo do workdir do projeto. Não passa pelo
// authGuard: quem autentica é o token assinado na query, porque a URL vive dentro
// de um iframe de origem opaca (sem cookie).
//
// O preview NUNCA deve ser embutido com allow-same-origin no painel: o HTML aqui é
// gerado por um modelo e tratado como não confiável.
func (s *Server) handleDesignPreview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := verifyPreviewToken(s.cfg.JWTSecret, id, r.URL.Query().Get("t")); err != nil {
		writeErr(w, err)
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

	rel := r.PathValue("path")
	if rel == "" {
		rel = designScreenRel()
	}
	full, err := safeDesignPath(workdir, rel)
	if err != nil {
		writeErr(w, err)
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Não encontrado"))
		return
	}

	ct := previewTypes[strings.ToLower(filepath.Ext(full))]
	if ct == "text/html; charset=utf-8" && r.URL.Query().Get("inspect") == "1" {
		data = injectInspector(data)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Security-Policy", designCSP(s.cfg))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// O conteúdo muda a cada turno e a URL carrega o sha; ainda assim, nada de cache
	// compartilhado: a URL tem token.
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
