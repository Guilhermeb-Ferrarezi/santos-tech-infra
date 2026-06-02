package main

import (
	"net/http"
	"time"
)

func (s *Server) registerRoutes(mux *http.ServeMux) {
	const min = time.Minute

	// Conversas (CRUD) — todas exigem admin.
	mux.HandleFunc("GET /claude/conversations", s.authGuard(s.handleListConversations))
	mux.HandleFunc("POST /claude/conversations", s.rateLimit(20, min, s.authGuard(s.handleCreateConversation)))
	mux.HandleFunc("GET /claude/conversations/{id}", s.authGuard(s.handleGetConversation))
	mux.HandleFunc("DELETE /claude/conversations/{id}", s.authGuard(s.handleDeleteConversation))

	// WebSocket de chat.
	mux.HandleFunc("GET /claude/conversations/{id}/ws", s.handleConversationWS)

	// Controles na camada de orquestração.
	mux.HandleFunc("POST /claude/conversations/{id}/model", s.authGuard(s.handleSetModel))
	mux.HandleFunc("POST /claude/conversations/{id}/compact", s.authGuard(s.handleCompact))
	mux.HandleFunc("POST /claude/conversations/{id}/clear", s.authGuard(s.handleClear))

	// OAuth do Claude (assinatura) — fluxo URL via PTY.
	mux.HandleFunc("POST /claude/auth/login", s.authGuard(s.handleClaudeAuthLogin))
	mux.HandleFunc("POST /claude/auth/callback", s.authGuard(s.handleClaudeAuthCallback))
	mux.HandleFunc("POST /claude/auth/logout", s.authGuard(s.handleClaudeAuthLogout))
	mux.HandleFunc("GET /claude/auth/status", s.authGuard(s.handleClaudeAuthStatus))
}
