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
	mux.HandleFunc("POST /auth/admin/users/{id}/send-reset", s.rateLimit(5, min, s.adminGuard(s.handleSendResetAdminUser)))
	// Excluir usuário é destrutivo: exige sudo mode (confirmação recente de identidade).
	mux.HandleFunc("DELETE /auth/admin/users/{id}", s.adminGuard(s.sudoGuard(s.handleDeleteAdminUser)))

	// Gestão admin de aplicações OAuth ("Entrar com Santos Tech")
	mux.HandleFunc("GET /auth/admin/oauth-clients", s.adminGuard(s.handleListOAuthClients))
	mux.HandleFunc("POST /auth/admin/oauth-clients", s.rateLimit(10, min, s.adminGuard(s.handleCreateOAuthClient)))
	mux.HandleFunc("PATCH /auth/admin/oauth-clients/{id}", s.rateLimit(20, min, s.adminGuard(s.handleUpdateOAuthClient)))
	mux.HandleFunc("DELETE /auth/admin/oauth-clients/{id}", s.adminGuard(s.sudoGuard(s.handleDeleteOAuthClient)))

	// Gestão admin de IPs banidos
	mux.HandleFunc("GET /auth/admin/ip-bans", s.adminGuard(s.handleListIPBans))
	mux.HandleFunc("POST /auth/admin/ip-bans", s.rateLimit(20, min, s.adminGuard(s.handleCreateIPBan)))
	mux.HandleFunc("DELETE /auth/admin/ip-bans/{id}", s.adminGuard(s.sudoGuard(s.handleDeleteIPBan)))

	// Gestão admin de cargos personalizados
	mux.HandleFunc("GET /auth/admin/custom-roles", s.adminGuard(s.handleListCustomRoles))
	mux.HandleFunc("POST /auth/admin/custom-roles", s.rateLimit(10, min, s.adminGuard(s.handleCreateCustomRole)))
	mux.HandleFunc("GET /auth/admin/custom-roles/{id}", s.adminGuard(s.handleGetCustomRole))
	mux.HandleFunc("PATCH /auth/admin/custom-roles/{id}", s.rateLimit(20, min, s.adminGuard(s.handleUpdateCustomRole)))
	mux.HandleFunc("DELETE /auth/admin/custom-roles/{id}", s.adminGuard(s.sudoGuard(s.handleDeleteCustomRole)))

	// Reset de senha (envia email — limite apertado)
	mux.HandleFunc("POST /auth/forgot-password", s.rateLimit(3, 5*min, s.handleForgotPassword))
	mux.HandleFunc("POST /auth/reset-password", s.rateLimit(10, min, s.handleResetPassword))

	// Verificação do email da conta (precisa de sessão; envia email — limite apertado)
	mux.HandleFunc("POST /auth/email-verify/send", s.rateLimit(3, 5*min, s.authGuard(s.handleEmailVerifySend)))
	mux.HandleFunc("POST /auth/email-verify/confirm", s.rateLimit(10, min, s.authGuard(s.handleEmailVerifyConfirm)))

	// MFA — gestão (precisa de sessão)
	mux.HandleFunc("POST /auth/mfa/setup", s.rateLimit(10, min, s.authGuard(s.handleMFASetup)))
	mux.HandleFunc("POST /auth/mfa/enable", s.rateLimit(10, min, s.authGuard(s.handleMFAEnable)))
	// Desabilitar MFA é sensível (remove o 2º fator) e checa TOTP/recovery/OTP:
	// rate-limit por IP na rota, além do teto por usuário dentro do handler.
	mux.HandleFunc("POST /auth/mfa/disable", s.rateLimit(10, min, s.authGuard(s.handleMFADisable)))
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

	// Logs do ecossistema (Loki) — admin-only. Responde 503 se LOKI_URL não configurado.
	mux.HandleFunc("GET /logs", s.rateLimit(30, min, s.adminGuard(s.handleLogs)))
	mux.HandleFunc("GET /logs/labels", s.rateLimit(30, min, s.adminGuard(s.handleLogLabels)))
	mux.HandleFunc("GET /logs/top-ips", s.rateLimit(20, min, s.adminGuard(s.handleTopIPs)))

	// Saúde agregada do ecossistema — autenticada (sessão ou PAT)
	mux.HandleFunc("GET /status", s.rateLimit(30, min, s.authGuard(s.handleStatus)))

	// Documentação das APIs pra agentes/devs — autenticada (sessão ou PAT)
	mux.HandleFunc("GET /llms.txt", s.rateLimit(30, min, s.authGuard(s.handleLLMsTxt)))

	// Quadros (Excalidraw) — admin/professor OU cargo personalizado (role 4) com
	// a permissão "quadros"; quadros são colaborativos, então professor cria/edita/
	// apaga (allowTeacher=true em write/delete). Cena salva com autosave (debounce
	// no front), por isso o limite de PUT é mais folgado.
	mux.HandleFunc("GET /boards", s.portalRead("quadros", s.handleListBoards))
	mux.HandleFunc("POST /boards", s.rateLimit(20, min, s.permGuard("quadros", "write", true, s.handleCreateBoard)))
	mux.HandleFunc("GET /boards/{id}", s.portalRead("quadros", s.handleGetBoard))
	mux.HandleFunc("PUT /boards/{id}", s.rateLimit(60, min, s.permGuard("quadros", "write", true, s.handleUpdateBoard)))
	mux.HandleFunc("DELETE /boards/{id}", s.permGuard("quadros", "delete", true, s.handleDeleteBoard))
	mux.HandleFunc("GET /boards/{id}/members", s.portalRead("quadros", s.handleListBoardMembers))
	mux.HandleFunc("POST /boards/{id}/members", s.rateLimit(20, min, s.permGuard("quadros", "write", true, s.handleAddBoardMember)))
	mux.HandleFunc("DELETE /boards/{id}/members/{userId}", s.permGuard("quadros", "write", true, s.handleRemoveBoardMember))

	// Calendário Editorial — admin CRUD completo; cargo social:read visualiza;
	// cargo social:execute atualiza status e notas.
	s.registerSocialRoutes(mux)

	// Portal admin/professor — overview, catálogo, turmas, matrículas e salas
	s.registerPortalRoutes(mux)

	// OAuth Google
	mux.HandleFunc("GET /auth/google", s.handleGoogleStart)
	mux.HandleFunc("GET /auth/google/callback", s.handleGoogleCallback)

	// OAuth provider ("Entrar com Santos Tech") — authorization code + PKCE
	mux.HandleFunc("GET /oauth/authorize", s.rateLimit(30, min, s.handleOAuthAuthorize))
	mux.HandleFunc("POST /oauth/authorize/confirm", s.rateLimit(20, min, s.handleOAuthConfirm))
	mux.HandleFunc("POST /oauth/token", s.rateLimit(20, min, s.handleOAuthToken))
	mux.HandleFunc("GET /oauth/userinfo", s.rateLimit(60, min, s.handleOAuthUserinfo))

	// Descoberta OAuth + DCR (clientes MCP, ex.: claude.ai) — públicos
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleOAuthASMetadata)
	mux.HandleFunc("GET /.well-known/openid-configuration", s.handleOIDCDiscovery)
	mux.HandleFunc("GET /.well-known/jwks.json", s.handleJWKS)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.handleProtectedResourceMCP)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.handleProtectedResourceMCP)
	mux.HandleFunc("POST /oauth/register", s.rateLimit(10, min, s.handleOAuthRegister))
}

func (s *Server) registerSocialRoutes(mux *http.ServeMux) {
	const min = time.Minute
	mux.HandleFunc("GET /social/posts", s.permGuard("social", "read", false, s.handleListSocialPosts))
	mux.HandleFunc("GET /social/posts/{id}", s.permGuard("social", "read", false, s.handleGetSocialPost))
	mux.HandleFunc("POST /social/posts", s.rateLimit(20, min, s.adminGuard(s.handleCreateSocialPost)))
	mux.HandleFunc("PUT /social/posts/{id}", s.rateLimit(30, min, s.adminGuard(s.handleUpdateSocialPost)))
	mux.HandleFunc("DELETE /social/posts/{id}", s.adminGuard(s.handleDeleteSocialPost))
	mux.HandleFunc("PATCH /social/posts/{id}/status", s.rateLimit(30, min, s.permGuard("social", "execute", false, s.handleUpdateSocialPostStatus)))
	mux.HandleFunc("GET /social/posts/{id}/notes", s.permGuard("social", "read", false, s.handleListSocialPostNotes))
	mux.HandleFunc("POST /social/posts/{id}/notes", s.rateLimit(30, min, s.permGuard("social", "execute", false, s.handleAddSocialPostNote)))
	mux.HandleFunc("GET /social/posts/{id}/history", s.permGuard("social", "read", false, s.handleListSocialPostStatusHistory))
}
