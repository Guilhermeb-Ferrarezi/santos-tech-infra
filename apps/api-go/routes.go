package main

import (
	"net/http"
	"time"
)

func (s *Server) registerAuthRoutes(mux *http.ServeMux) {
	const min = time.Minute

	// Core (rotas sensíveis com rate limit por IP)
	mux.HandleFunc("POST /auth/register", s.rateLimit(5, min, s.handleRegister))
	mux.HandleFunc("POST /auth/login", s.rateLimit(10, min, s.handleLogin))
	mux.HandleFunc("POST /auth/logout", s.handleLogout)
	mux.HandleFunc("GET /auth/logout", s.handleLogoutGet)
	mux.HandleFunc("GET /auth/me", s.handleMe)
	mux.HandleFunc("POST /auth/refresh", s.rateLimit(20, min, s.handleRefresh))

	// Preferências de UI do usuário (merge no JSONB users.preferences — precisa de sessão)
	mux.HandleFunc("PATCH /auth/me/preferences", s.rateLimit(30, min, s.authGuard(s.handlePreferencesUpdate)))

	// Upload de avatar (multipart → R2 → users.avatar_url; precisa de sessão)
	mux.HandleFunc("POST /auth/avatar", s.rateLimit(10, min, s.authGuard(s.handleAvatarUpload)))

	// Upload genérico de imagem (multipart → R2 → devolve URL; ex: foto de remetente)
	mux.HandleFunc("POST /auth/upload", s.rateLimit(10, min, s.authGuard(s.handleImageUpload)))

	// API tokens (PAT) — gestão da própria conta (precisa de sessão).
	// Criar token é sensível (vira credencial de longa duração): exige sudo mode.
	mux.HandleFunc("POST /auth/api-keys", s.rateLimit(10, min, s.authGuard(s.sudoGuard(s.handleCreateAPIKey))))
	mux.HandleFunc("GET /auth/api-keys", s.authGuard(s.handleListAPIKeys))
	mux.HandleFunc("DELETE /auth/api-keys/{id}", s.authGuard(s.handleDeleteAPIKey))

	// Gestão admin de usuários @santos-tech.com (exige role=Admin)
	mux.HandleFunc("GET /auth/admin/users", s.adminGuard(s.handleListAdminUsers))
	mux.HandleFunc("POST /auth/admin/users", s.rateLimit(10, min, s.adminGuard(s.handleCreateAdminUser)))
	mux.HandleFunc("PATCH /auth/admin/users/{id}", s.rateLimit(20, min, s.adminGuard(s.handleUpdateAdminUser)))
	mux.HandleFunc("POST /auth/admin/users/{id}/invite", s.rateLimit(5, min, s.adminGuard(s.handleInviteAdminUser)))
	// Excluir usuário é destrutivo: exige sudo mode (confirmação recente de identidade).
	mux.HandleFunc("DELETE /auth/admin/users/{id}", s.adminGuard(s.sudoGuard(s.handleDeleteAdminUser)))

	// Reset de senha (envia email — limite apertado)
	mux.HandleFunc("POST /auth/forgot-password", s.rateLimit(3, 5*min, s.handleForgotPassword))
	mux.HandleFunc("POST /auth/reset-password", s.rateLimit(10, min, s.handleResetPassword))

	// Verificação do email da conta (precisa de sessão; envia email — limite apertado)
	mux.HandleFunc("POST /auth/email-verify/send", s.rateLimit(3, 5*min, s.authGuard(s.handleEmailVerifySend)))
	mux.HandleFunc("POST /auth/email-verify/confirm", s.rateLimit(10, min, s.authGuard(s.handleEmailVerifyConfirm)))

	// MFA — gestão (precisa de sessão)
	mux.HandleFunc("POST /auth/mfa/setup", s.authGuard(s.handleMFASetup))
	mux.HandleFunc("POST /auth/mfa/enable", s.authGuard(s.handleMFAEnable))
	mux.HandleFunc("POST /auth/mfa/disable", s.authGuard(s.handleMFADisable))
	// MFA por email — código pra conta logada (ativar/desativar/sudo) e ativação
	mux.HandleFunc("POST /auth/mfa/email-code", s.rateLimit(3, 5*min, s.authGuard(s.handleMFAEmailCode)))
	mux.HandleFunc("POST /auth/mfa/email-enable", s.rateLimit(10, min, s.authGuard(s.handleMFAEmailEnable)))
	mux.HandleFunc("POST /auth/mfa/method", s.rateLimit(10, min, s.authGuard(s.handleMFAMethod)))
	// MFA — 2º passo do login (público; envia email — limite apertado)
	mux.HandleFunc("POST /auth/mfa/email", s.rateLimit(3, 5*min, s.handleMFAEmail))
	mux.HandleFunc("POST /auth/mfa/verify", s.rateLimit(10, min, s.handleMFAVerify))

	// Sudo mode — confirma a identidade pra ações sensíveis (eleva por 15min)
	mux.HandleFunc("POST /auth/sudo/verify", s.rateLimit(10, min, s.authGuard(s.handleSudoVerify)))

	// Multi-conta no navegador (cookie assinado "accounts" — chooser estilo Google)
	mux.HandleFunc("GET /auth/accounts", s.handleAccountsList)
	mux.HandleFunc("DELETE /auth/accounts/{sessionId}", s.handleAccountDelete)
	mux.HandleFunc("POST /auth/accounts/{sessionId}/activate", s.rateLimit(20, min, s.handleAccountActivate))

	// Saúde agregada do ecossistema — autenticada (sessão ou PAT)
	mux.HandleFunc("GET /status", s.rateLimit(30, min, s.authGuard(s.handleStatus)))

	// Documentação das APIs pra agentes/devs — autenticada (sessão ou PAT)
	mux.HandleFunc("GET /llms.txt", s.rateLimit(30, min, s.authGuard(s.handleLLMsTxt)))

	// OAuth Google
	mux.HandleFunc("GET /auth/google", s.handleGoogleStart)
	mux.HandleFunc("GET /auth/google/callback", s.handleGoogleCallback)
}
