package main

import "net/http"

// registerAuthRoutes monta as rotas de auth (adicionadas por bloco).
func (s *Server) registerAuthRoutes(mux *http.ServeMux) {
	// Core
	mux.HandleFunc("POST /auth/register", s.handleRegister)
	mux.HandleFunc("POST /auth/login", s.handleLogin)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)
	mux.HandleFunc("GET /auth/me", s.handleMe)
	mux.HandleFunc("POST /auth/refresh", s.handleRefresh)

	// Reset de senha
	mux.HandleFunc("POST /auth/forgot-password", s.handleForgotPassword)
	mux.HandleFunc("POST /auth/reset-password", s.handleResetPassword)

	// MFA — gestão (precisa de sessão)
	mux.HandleFunc("POST /auth/mfa/setup", s.authGuard(s.handleMFASetup))
	mux.HandleFunc("POST /auth/mfa/enable", s.authGuard(s.handleMFAEnable))
	mux.HandleFunc("POST /auth/mfa/disable", s.authGuard(s.handleMFADisable))
	// MFA — 2º passo do login (público; usa o challenge)
	mux.HandleFunc("POST /auth/mfa/email", s.handleMFAEmail)
	mux.HandleFunc("POST /auth/mfa/verify", s.handleMFAVerify)

	// OAuth Google
	mux.HandleFunc("GET /auth/google", s.handleGoogleStart)
	mux.HandleFunc("GET /auth/google/callback", s.handleGoogleCallback)
}
