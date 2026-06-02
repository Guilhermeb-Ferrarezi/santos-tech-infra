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
	mux.HandleFunc("PATCH /claude/conversations/{id}", s.authGuard(s.handleRenameConversation))
	mux.HandleFunc("DELETE /claude/conversations/{id}", s.authGuard(s.handleDeleteConversation))

	// Kill switch — para todos os turnos em andamento.
	mux.HandleFunc("POST /claude/stop-all", s.authGuard(s.handleStopAll))

	// WebSocket de chat.
	mux.HandleFunc("GET /claude/conversations/{id}/ws", s.handleConversationWS)

	// Controles na camada de orquestração.
	mux.HandleFunc("POST /claude/conversations/{id}/model", s.authGuard(s.handleSetModel))
	mux.HandleFunc("POST /claude/conversations/{id}/compact", s.authGuard(s.handleCompact))
	mux.HandleFunc("POST /claude/conversations/{id}/clear", s.authGuard(s.handleClear))

	// Login do app mobile (troca email/senha por token JSON).
	mux.HandleFunc("POST /claude/auth/session", s.rateLimit(10, min, s.handleMobileSession))

	// Push notifications (registra Expo push token do dispositivo).
	mux.HandleFunc("POST /claude/push/register", s.authGuard(s.handlePushRegister))

	// OAuth do Claude (assinatura) — fluxo URL via PTY.
	mux.HandleFunc("POST /claude/auth/login", s.authGuard(s.handleClaudeAuthLogin))
	mux.HandleFunc("POST /claude/auth/callback", s.authGuard(s.handleClaudeAuthCallback))
	mux.HandleFunc("POST /claude/auth/logout", s.authGuard(s.handleClaudeAuthLogout))
	mux.HandleFunc("GET /claude/auth/status", s.authGuard(s.handleClaudeAuthStatus))
}
