package main

import (
	_ "embed"
	"net/http"
)

// llms.txt: documentação das APIs do ecossistema para agentes (Claude) e devs.
// Embutida no binário e servida AUTENTICADA — sessão (cookie/Bearer) ou PAT.
//
//go:embed llms.txt
var llmsTxt []byte

// GET /llms.txt — devolve a documentação (precisa de sessão; não é pública).
func (s *Server) handleLLMsTxt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, private")
	_, _ = w.Write(llmsTxt)
}
