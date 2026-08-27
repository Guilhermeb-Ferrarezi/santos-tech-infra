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

	// Web Push — subscription do navegador atual (gestão da própria conta,
	// precisa de sessão). Nova tarefa / novo email disparam envio via
	// enqueuePush (ver queue.go); webhook de email novo fica em
	// registerInstagramRoutes-style, sem authGuard (ver routes abaixo).
	mux.HandleFunc("POST /auth/push/subscribe", s.rateLimit(20, min, s.authGuard(s.handlePushSubscribe)))
	mux.HandleFunc("DELETE /auth/push/subscribe", s.rateLimit(20, min, s.authGuard(s.handlePushUnsubscribe)))

	// Central de notificações (sino no header) — histórico + marcar como lida.
	// Rate limit folgado no GET: o sino faz polling periódico do front.
	mux.HandleFunc("GET /auth/notifications", s.rateLimit(60, min, s.authGuard(s.handleListNotifications)))
	mux.HandleFunc("POST /auth/notifications/{id}/read", s.rateLimit(60, min, s.authGuard(s.handleMarkNotificationRead)))
	mux.HandleFunc("POST /auth/notifications/read-all", s.rateLimit(20, min, s.authGuard(s.handleMarkAllNotificationsRead)))

	// Upload de avatar (multipart → R2 → users.avatar_url; precisa de sessão)
	mux.HandleFunc("POST /auth/avatar", s.rateLimit(10, min, s.authGuard(s.handleAvatarUpload)))

	// Upload genérico de imagem (multipart → R2 → devolve URL; ex: foto de remetente)
	mux.HandleFunc("POST /auth/upload", s.rateLimit(10, min, s.authGuard(s.handleImageUpload)))

	// URL pré-assinada pra upload direto de vídeo no R2 (cliente faz o PUT, sem
	// passar pelo backend — arquivo grande demais pra bufferizar em memória)
	mux.HandleFunc("POST /auth/upload/presigned", s.rateLimit(10, min, s.authGuard(s.handleVideoPresign)))

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
	// Libera um client pendente (DCR anônimo nasce inativo — ver handlers_oauth_discovery.go).
	mux.HandleFunc("POST /auth/admin/oauth-clients/{id}/approve", s.rateLimit(20, min, s.adminGuard(s.sudoGuard(s.handleApproveOAuthClient))))
	mux.HandleFunc("DELETE /auth/admin/oauth-clients/{id}", s.adminGuard(s.sudoGuard(s.handleDeleteOAuthClient)))

	// Gestão admin de IPs banidos
	mux.HandleFunc("GET /auth/admin/ip-bans", s.adminGuard(s.handleListIPBans))
	mux.HandleFunc("POST /auth/admin/ip-bans", s.rateLimit(20, min, s.adminGuard(s.handleCreateIPBan)))
	mux.HandleFunc("DELETE /auth/admin/ip-bans/{id}", s.adminGuard(s.sudoGuard(s.handleDeleteIPBan)))

	// Biblioteca admin de modelos 3D (upload/listagem/edição/exclusão via R2 + Postgres)
	mux.HandleFunc("GET /auth/admin/models3d", s.adminGuard(s.handleListModel3D))
	mux.HandleFunc("POST /auth/admin/models3d", s.rateLimit(10, min, s.adminGuard(s.handleUploadModel3D)))
	mux.HandleFunc("PATCH /auth/admin/models3d/{id}", s.rateLimit(30, min, s.adminGuard(s.handlePatchModel3D)))
	mux.HandleFunc("DELETE /auth/admin/models3d/{id}", s.adminGuard(s.handleDeleteModel3D))

	// Roteador de chaves de API: cadastro de credenciais reais de provedores
	// externos, com failover automático em 401/sem-créditos (ver apirouter.go).
	// Admin-only, sem cargo personalizado (mesmo critério de ip-bans/oauth-clients).
	// Listar não expõe segredo (só secretTail); criar/apagar/revelar chave
	// manipula ou expõe credencial real -> sudo.
	mux.HandleFunc("GET /auth/admin/api-router/providers", s.adminGuard(s.handleListAPIRouterProviders))
	mux.HandleFunc("POST /auth/admin/api-router/providers", s.rateLimit(10, min, s.adminGuard(s.handleCreateAPIRouterProvider)))
	mux.HandleFunc("PATCH /auth/admin/api-router/providers/{id}", s.rateLimit(20, min, s.adminGuard(s.handleUpdateAPIRouterProvider)))
	mux.HandleFunc("DELETE /auth/admin/api-router/providers/{id}", s.adminGuard(s.sudoGuard(s.handleDeleteAPIRouterProvider)))
	mux.HandleFunc("GET /auth/admin/api-router/providers/{id}/keys", s.adminGuard(s.handleListAPIRouterKeys))
	// Revelar o segredo cru de UMA chave: sudo + auditoria. A listagem acima
	// devolve só secretTail — o valor completo só sai por aqui.
	mux.HandleFunc("GET /auth/admin/api-router/providers/{id}/keys/{keyId}/reveal", s.rateLimit(10, min, s.adminGuard(s.sudoGuard(s.handleRevealAPIRouterKey))))
	mux.HandleFunc("POST /auth/admin/api-router/providers/{id}/keys", s.rateLimit(10, min, s.adminGuard(s.sudoGuard(s.handleCreateAPIRouterKey))))
	mux.HandleFunc("PATCH /auth/admin/api-router/providers/{id}/keys/{keyId}", s.rateLimit(20, min, s.adminGuard(s.handleUpdateAPIRouterKey)))
	mux.HandleFunc("DELETE /auth/admin/api-router/providers/{id}/keys/{keyId}", s.adminGuard(s.sudoGuard(s.handleDeleteAPIRouterKey)))
	mux.HandleFunc("POST /auth/admin/api-router/providers/{id}/keys/{keyId}/status", s.rateLimit(20, min, s.adminGuard(s.handleSetAPIRouterKeyStatus)))
	mux.HandleFunc("POST /auth/admin/api-router/providers/{id}/keys/{keyId}/test", s.rateLimit(20, min, s.adminGuard(s.handleTestAPIRouterKey)))
	// Proxy real (uso de verdade da API, não só teste de chave): repassa
	// method/path/body pro provider com rotação automática, devolve a
	// resposta tal como veio. Sem streaming.
	mux.HandleFunc("POST /auth/admin/api-router/providers/{id}/proxy", s.rateLimit(20, min, s.adminGuard(s.handleAPIRouterProxy)))
	// Chat normalizado: mesma ideia do /proxy, mas monta a requisição nativa via
	// provider.chatAdapter (openai_compatible/anthropic/google/cohere) e devolve
	// só o texto — pro admin não precisar saber o formato de cada API.
	mux.HandleFunc("POST /auth/admin/api-router/providers/{id}/chat", s.rateLimit(10, min, s.adminGuard(s.handleAPIRouterChat)))
	// Operações normalizadas (transcribe/tts/image/predict/voices): monta o
	// request nativo via provider.opAdapter e devolve o resultado extraído —
	// AssemblyAI/Replicate rodam com polling interno do job.
	mux.HandleFunc("POST /auth/admin/api-router/providers/{id}/op/{op}", s.rateLimit(10, min, s.adminGuard(s.handleAPIRouterOp)))
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

	// Erros do ecossistema (Sentry) — admin-only. Responde 503 se SENTRY_API_TOKEN não configurado.
	mux.HandleFunc("GET /sentry/issues", s.rateLimit(30, min, s.adminGuard(s.handleSentryIssues)))
	mux.HandleFunc("GET /sentry/issues/{id}", s.rateLimit(60, min, s.adminGuard(s.handleSentryIssueDetail)))
	mux.HandleFunc("GET /sentry/projects", s.rateLimit(30, min, s.adminGuard(s.handleSentryProjects)))

	// Analytics do ecossistema (PostHog) — admin-only. Responde 503 se
	// POSTHOG_PERSONAL_API_KEY não configurado.
	mux.HandleFunc("GET /analytics/overview", s.rateLimit(30, min, s.adminGuard(s.handlePostHogOverview)))
	mux.HandleFunc("GET /analytics/timeseries", s.rateLimit(30, min, s.adminGuard(s.handlePostHogTimeseries)))
	mux.HandleFunc("GET /analytics/top-pages", s.rateLimit(30, min, s.adminGuard(s.handlePostHogTopPages)))
	mux.HandleFunc("GET /analytics/referrers", s.rateLimit(30, min, s.adminGuard(s.handlePostHogReferrers)))
	mux.HandleFunc("GET /analytics/by-site", s.rateLimit(30, min, s.adminGuard(s.handlePostHogBySite)))
	mux.HandleFunc("GET /analytics/breakdown", s.rateLimit(30, min, s.adminGuard(s.handlePostHogBreakdown)))
	// Replay: eventos podem levar alguns segundos e chegar a alguns MB numa
	// sessão longa — rate limit mais apertado que os outros endpoints leves.
	mux.HandleFunc("GET /analytics/recordings", s.rateLimit(30, min, s.adminGuard(s.handlePostHogRecordings)))
	mux.HandleFunc("GET /analytics/recordings/{id}/events", s.rateLimit(10, min, s.adminGuard(s.handlePostHogRecordingEvents)))

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

	// Blog público (santos-tech.com/blog) — leitura sem auth; CRUD admin/cargo
	// personalizado com blog_posts:read/write.
	s.registerBlogRoutes(mux)

	// Vitrine de links (Linktree institucional) — CRUD admin/cargo
	// personalizado com links:read/write; leitura pública em /public/links
	// alimenta santos-tech.com/links (Santos-Tech-Home-Page).
	s.registerLinkShowcaseRoutes(mux)

	// Controle de horas de clientes (lan house/escola) — CRUD admin-only
	// (dado financeiro); leitura + pedido de pausa públicos por token em
	// /public/hour-sessions/{token}, consumidos pela rota /sessao/:token do
	// dashboard/web (sem login).
	s.registerHourSessionRoutes(mux)

	// Identificação/controle dos PCs do laboratório (hour-timer-app): nome
	// atribuído pelo admin, heartbeat público por device_uuid, comandos
	// (despairar/mandar aviso) entregues no heartbeat seguinte.
	s.registerLabDeviceRoutes(mux)

	// Catálogo de downloads (Santos Hub — app Tauri Windows distribuído nos PCs
	// da empresa): CRUD admin-only; leitura pública sem login em
	// /public/downloads (o app não pede autenticação nenhuma).
	s.registerDownloadsRoutes(mux)

	// Login por QR code / código curto (estilo WhatsApp Web, igual ao pareamento
	// dos PCs do laboratório): o Santos Hub (sem sessão) gera e MOSTRA o QR/
	// código; quem já está logado (celular escaneando, ou outra aba) confirma
	// em /auth/qr-login/confirm; o Hub só fica com poll em /public/qr-login/poll
	// até aprovar. Uso único, 5min de vida.
	mux.HandleFunc("POST /public/qr-login/create", s.rateLimit(20, min, s.handleQRLoginCreate))
	mux.HandleFunc("POST /auth/qr-login/confirm", s.rateLimit(20, min, s.authGuard(s.handleQRLoginConfirm)))
	mux.HandleFunc("GET /public/qr-login/poll", s.rateLimit(60, min, s.handleQRLoginPoll))

	// Automação de resposta a comentário do Instagram (private reply,
	// substitui o ManyChat) — webhook público autenticado por assinatura
	// Meta (não cookie/PAT) + CRUD admin do mapeamento post -> link.
	s.registerInstagramRoutes(mux)

	// Arquivos (pastas vinculadas ao Google Drive) — CRUD de pasta e ACL são
	// admin-only; leitura/envio de arquivo usa acesso resolvido por PASTA (cargo
	// fixo/personalizado OU usuário individual — não um resource fixo de cargo),
	// ver drive_access.go.
	s.registerDriveRoutes(mux)

	// Webhook de "email novo" — chamado pelo docker-mailserver (Contabo, repo
	// email/), não por um usuário logado. Autenticado por assinatura HMAC
	// compartilhada (EMAIL_WEBHOOK_SECRET), sem authGuard/adminGuard.
	mux.HandleFunc("POST /webhooks/email/new", s.rateLimit(120, min, s.handleEmailWebhook))

	// OAuth Google
	mux.HandleFunc("GET /auth/google", s.rateLimit(20, min, s.handleGoogleStart))
	mux.HandleFunc("GET /auth/google/callback", s.rateLimit(20, min, s.handleGoogleCallback))

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
	mux.HandleFunc("POST /social/posts/{id}/publish", s.rateLimit(10, min, s.permGuard("social", "execute", false, s.handlePublishSocialPost)))
	mux.HandleFunc("POST /social/posts/{id}/publish-confirmations/{platform}", s.rateLimit(60, min, s.permGuard("social", "execute", false, s.handleConfirmSocialPostPlatform)))
	mux.HandleFunc("DELETE /social/posts/{id}/publish-confirmations/{platform}", s.rateLimit(60, min, s.permGuard("social", "execute", false, s.handleUnconfirmSocialPostPlatform)))
	mux.HandleFunc("GET /social/platform-owners", s.permGuard("social", "read", false, s.handleListSocialPlatformOwners))
	mux.HandleFunc("PUT /social/platform-owners/{platform}", s.rateLimit(30, min, s.adminGuard(s.handleSetSocialPlatformOwner)))
	mux.HandleFunc("DELETE /social/platform-owners/{platform}", s.adminGuard(s.handleDeleteSocialPlatformOwner))
	mux.HandleFunc("GET /social/settings", s.permGuard("social", "read", false, s.handleGetSocialSettings))
	mux.HandleFunc("PUT /social/settings", s.rateLimit(30, min, s.adminGuard(s.handleUpdateSocialSettings)))
	// Série: lista fechada de linhas de conteúdo do calendário — leitura pra
	// quem já vê o calendário, cadastro/edição só admin (mesma convenção de
	// platform-owners/settings acima).
	mux.HandleFunc("GET /social/series", s.permGuard("social", "read", false, s.handleListSocialSeries))
	mux.HandleFunc("POST /social/series", s.rateLimit(20, min, s.adminGuard(s.handleCreateSocialSerie)))
	mux.HandleFunc("PUT /social/series/{id}", s.rateLimit(30, min, s.adminGuard(s.handleUpdateSocialSerie)))
	mux.HandleFunc("GET /tasks", s.permGuard("tarefas", "read", true, s.handleListTasks))
	mux.HandleFunc("GET /tasks/{id}", s.permGuard("tarefas", "read", true, s.handleGetTask))
	mux.HandleFunc("POST /tasks", s.rateLimit(30, min, s.permGuard("tarefas", "write", true, s.handleCreateTask)))
	mux.HandleFunc("PUT /tasks/{id}", s.rateLimit(30, min, s.permGuard("tarefas", "write", true, s.handleUpdateTask)))
	mux.HandleFunc("DELETE /tasks/{id}", s.permGuard("tarefas", "delete", true, s.handleDeleteTask))
	mux.HandleFunc("GET /tasks/{id}/notes", s.permGuard("tarefas", "read", true, s.handleListTaskNotes))
	mux.HandleFunc("POST /tasks/{id}/notes", s.rateLimit(30, min, s.permGuard("tarefas", "write", true, s.handleAddTaskNote)))
	// DELETE /tasks/{id}/confirm colidiria (ambiguidade do net/http.ServeMux) com
	// DELETE /tasks/categories/{id} já registrada — "/tasks/categories/confirm"
	// bate nos dois padrões e nenhum é mais específico (Go recusa subir o
	// processo nesse caso). POST evita a colisão (não existe POST /tasks/categories/{id}).
	mux.HandleFunc("POST /tasks/{id}/confirm", s.rateLimit(30, min, s.permGuard("tarefas", "write", true, s.handleConfirmTask)))
	mux.HandleFunc("POST /tasks/{id}/unconfirm", s.rateLimit(30, min, s.permGuard("tarefas", "write", true, s.handleUnconfirmTask)))
	mux.HandleFunc("GET /tasks/categories", s.permGuard("tarefas", "read", true, s.handleListTaskCategories))
	mux.HandleFunc("POST /tasks/categories", s.adminGuard(s.handleCreateTaskCategory))
	mux.HandleFunc("PUT /tasks/categories/{id}", s.adminGuard(s.handleUpdateTaskCategory))
	mux.HandleFunc("DELETE /tasks/categories/{id}", s.adminGuard(s.handleDeleteTaskCategory))

	mux.HandleFunc("GET /agenda/eventos", s.permGuard("agenda", "read", true, s.handleListAgendaEventos))
	mux.HandleFunc("GET /agenda/eventos/{id}", s.permGuard("agenda", "read", true, s.handleGetAgendaEvento))
	mux.HandleFunc("POST /agenda/eventos", s.rateLimit(30, min, s.permGuard("agenda", "write", true, s.handleCreateAgendaEvento)))
	mux.HandleFunc("PUT /agenda/eventos/{id}", s.rateLimit(30, min, s.permGuard("agenda", "write", true, s.handleUpdateAgendaEvento)))
	mux.HandleFunc("DELETE /agenda/eventos/{id}", s.permGuard("agenda", "delete", true, s.handleDeleteAgendaEvento))
	mux.HandleFunc("POST /agenda/eventos/check", s.rateLimit(60, min, s.permGuard("agenda", "write", true, s.handleCheckAgendaEvento)))
	mux.HandleFunc("GET /agenda/feriados", s.permGuard("agenda", "read", true, s.handleListAgendaFeriados))
	mux.HandleFunc("GET /agenda/feriados-municipais", s.adminGuard(s.handleListAgendaFeriadosMunicipais))
	mux.HandleFunc("POST /agenda/feriados-municipais", s.rateLimit(20, min, s.adminGuard(s.handleCreateAgendaFeriadoMunicipal)))
	mux.HandleFunc("DELETE /agenda/feriados-municipais/{id}", s.adminGuard(s.handleDeleteAgendaFeriadoMunicipal))

	mux.HandleFunc("GET /glossary", s.authGuard(s.handleListGlossaryTerms))
	mux.HandleFunc("POST /glossary", s.rateLimit(30, min, s.adminGuard(s.handleCreateGlossaryTerm)))
	mux.HandleFunc("PUT /glossary/{id}", s.rateLimit(30, min, s.adminGuard(s.handleUpdateGlossaryTerm)))
	mux.HandleFunc("DELETE /glossary/{id}", s.adminGuard(s.handleDeleteGlossaryTerm))
}

func (s *Server) registerBlogRoutes(mux *http.ServeMux) {
	const min = time.Minute

	// Público — sem guard nenhum (igual /health), só rate limit por IP.
	mux.HandleFunc("GET /public/blog/posts", s.rateLimit(120, min, s.handleListPublicBlogPosts))
	mux.HandleFunc("GET /public/blog/posts/{slug}", s.rateLimit(120, min, s.handleGetPublicBlogPost))
	mux.HandleFunc("GET /public/blog/categories", s.rateLimit(120, min, s.handleListBlogCategories))

	// Beacon de analytics do blog — público, sem auth, rate limit mais folgado
	// que leitura normal (é chamado a cada pageview + cada clique de CTA).
	mux.HandleFunc("POST /public/blog/events", s.rateLimit(300, min, s.handleBlogEventIngest))

	// Heatmap do blog — ingestão pública em LOTE (client acumula cliques +
	// profundidade de scroll e manda 1 request por flush, nunca por clique;
	// ver blog/web/src/lib/blog-heatmap.ts). Agregação admin exige
	// postSlug+viewport: heatmap não faz sentido agregado entre posts.
	mux.HandleFunc("POST /public/blog/heatmap/events", s.rateLimit(120, min, s.handleBlogHeatmapIngest))
	mux.HandleFunc("GET /blog/metrics/heatmap/clicks", s.permGuard("blog_posts", "read", false, s.handleBlogHeatmapClicks))
	mux.HandleFunc("GET /blog/metrics/heatmap/scroll", s.permGuard("blog_posts", "read", false, s.handleBlogHeatmapScroll))

	// Admin — admin ou cargo personalizado com blog_posts:read/write; DELETE
	// (destrutivo) fica admin-only + sudo, fora do sistema de permissão de cargo.
	mux.HandleFunc("GET /blog/posts", s.permGuard("blog_posts", "read", false, s.handleListBlogPosts))
	mux.HandleFunc("GET /blog/posts/{id}", s.permGuard("blog_posts", "read", false, s.handleGetBlogPost))
	mux.HandleFunc("POST /blog/posts", s.rateLimit(30, min, s.permGuard("blog_posts", "write", false, s.handleCreateBlogPost)))
	mux.HandleFunc("PATCH /blog/posts/{id}", s.rateLimit(60, min, s.permGuard("blog_posts", "write", false, s.handleUpdateBlogPost)))
	mux.HandleFunc("DELETE /blog/posts/{id}", s.adminGuard(s.sudoGuard(s.handleDeleteBlogPost)))

	mux.HandleFunc("GET /blog/categories", s.permGuard("blog_posts", "read", false, s.handleListBlogCategoriesAdmin))
	mux.HandleFunc("POST /blog/categories", s.rateLimit(20, min, s.permGuard("blog_posts", "write", false, s.handleCreateBlogCategory)))
	mux.HandleFunc("PATCH /blog/categories/{id}", s.rateLimit(20, min, s.permGuard("blog_posts", "write", false, s.handleUpdateBlogCategory)))
	mux.HandleFunc("DELETE /blog/categories/{id}", s.adminGuard(s.sudoGuard(s.handleDeleteBlogCategory)))

	// Métricas do blog — admin, mesma permissão de quem já lê posts (blog_posts:read).
	// Rate limit: queries de analytics fazem table-scan em blog_events; 30/min por IP
	// é mais que suficiente para o painel e impede uso abusivo (mesmo por admins).
	mux.HandleFunc("GET /blog/metrics/overview", s.rateLimit(30, min, s.permGuard("blog_posts", "read", false, s.handleBlogMetricsOverview)))
	mux.HandleFunc("GET /blog/metrics/timeseries", s.rateLimit(30, min, s.permGuard("blog_posts", "read", false, s.handleBlogMetricsTimeseries)))
	mux.HandleFunc("GET /blog/metrics/top-posts", s.rateLimit(30, min, s.permGuard("blog_posts", "read", false, s.handleBlogMetricsTopPosts)))
	mux.HandleFunc("GET /blog/metrics/referrers", s.rateLimit(30, min, s.permGuard("blog_posts", "read", false, s.handleBlogMetricsReferrers)))
	mux.HandleFunc("GET /blog/metrics/utm-source", s.rateLimit(30, min, s.permGuard("blog_posts", "read", false, s.handleBlogMetricsUTMSource)))
	mux.HandleFunc("GET /blog/metrics/devices", s.rateLimit(30, min, s.permGuard("blog_posts", "read", false, s.handleBlogMetricsDevices)))
	mux.HandleFunc("GET /blog/metrics/countries", s.rateLimit(30, min, s.permGuard("blog_posts", "read", false, s.handleBlogMetricsCountries)))
}

func (s *Server) registerLinkShowcaseRoutes(mux *http.ServeMux) {
	const min = time.Minute
	mux.HandleFunc("GET /links", s.permGuard("links", "read", true, s.handleListLinkShowcaseItems))
	mux.HandleFunc("POST /links", s.rateLimit(30, min, s.permGuard("links", "write", false, s.handleCreateLinkShowcaseItem)))
	mux.HandleFunc("PUT /links/{id}", s.rateLimit(30, min, s.permGuard("links", "write", false, s.handleUpdateLinkShowcaseItem)))
	mux.HandleFunc("DELETE /links/{id}", s.permGuard("links", "write", false, s.handleDeleteLinkShowcaseItem))

	// Config da página inteira (hoje só imagem de fundo) — "settings" é
	// literal e o net/http mux (Go 1.22+) prefere segmento literal sobre
	// {id}, então não colide com PUT /links/{id} acima.
	mux.HandleFunc("GET /links/settings", s.permGuard("links", "read", true, s.handleGetLinkShowcaseSettings))
	mux.HandleFunc("PUT /links/settings", s.rateLimit(30, min, s.permGuard("links", "write", false, s.handleUpdateLinkShowcaseSettings)))

	// Público — sem guard nenhum (igual /public/blog/posts), só rate limit por IP.
	mux.HandleFunc("GET /public/links", s.rateLimit(120, min, s.handleListPublicLinkShowcaseItems))
}

// registerHourSessionRoutes: controle de horas de clientes. Admin-only (dado
// financeiro, sem cargo personalizado por enquanto) + rota pública por token
// pro link que o cliente abre (sem sessão/cookie).
func (s *Server) registerHourSessionRoutes(mux *http.ServeMux) {
	const min = time.Minute
	mux.HandleFunc("GET /hour-clients", s.adminGuard(s.handleListHourClients))
	mux.HandleFunc("POST /hour-clients", s.rateLimit(20, min, s.adminGuard(s.handleCreateHourClient)))
	mux.HandleFunc("POST /hour-clients/{id}/purchases", s.rateLimit(30, min, s.adminGuard(s.handleAddHourPurchase)))
	mux.HandleFunc("PATCH /hour-clients/{id}", s.rateLimit(30, min, s.adminGuard(s.handleUpdateHourClientDiscount)))

	mux.HandleFunc("GET /hour-billing", s.adminGuard(s.handleGetHourBilling))

	mux.HandleFunc("GET /hour-sessions", s.adminGuard(s.handleListHourSessions))
	mux.HandleFunc("POST /hour-sessions", s.rateLimit(30, min, s.adminGuard(s.handleStartHourSession)))
	mux.HandleFunc("POST /hour-sessions/{id}/pause", s.rateLimit(60, min, s.adminGuard(s.handlePauseHourSession)))
	mux.HandleFunc("POST /hour-sessions/{id}/resume", s.rateLimit(60, min, s.adminGuard(s.handleResumeHourSession)))
	mux.HandleFunc("POST /hour-sessions/{id}/end", s.rateLimit(60, min, s.adminGuard(s.handleEndHourSession)))
	mux.HandleFunc("POST /hour-sessions/{id}/deny-pause", s.rateLimit(60, min, s.adminGuard(s.handleDenyHourSessionPause)))
	mux.HandleFunc("POST /hour-sessions/{id}/link", s.rateLimit(30, min, s.adminGuard(s.handleReissueHourSessionLink)))
	// Histórico (quem pausou/retomou, quando) e correção manual de tempo — o
	// ajuste entra como evento, então aparece no próprio histórico.
	mux.HandleFunc("GET /hour-sessions/{id}/events", s.adminGuard(s.handleListHourSessionEvents))
	mux.HandleFunc("POST /hour-sessions/{id}/adjust", s.rateLimit(30, min, s.adminGuard(s.handleAdjustHourSession)))

	// Público — identificado pelo token da sessão (posse == acesso), não por
	// cookie/sessão. GET tem rate limit folgado (o front faz polling); o
	// pedido de pausa é mais restrito pra não virar canal de spam pro admin.
	mux.HandleFunc("GET /public/hour-sessions/{token}", s.rateLimit(120, min, s.handleGetPublicHourSession))
	mux.HandleFunc("POST /public/hour-sessions/{token}/request-pause", s.rateLimit(5, min, s.handleRequestHourSessionPause))
	// Código curto (6 dígitos) como alternativa a colar o link no PC — rate
	// limit apertado pra não virar canal de força-bruta (código expira em 15min
	// e é de uso único, mas ainda assim vale limitar tentativas por IP).
	mux.HandleFunc("POST /public/hour-sessions/pair-by-code", s.rateLimit(15, min, s.handlePairHourSessionByCode))
}

// registerLabDeviceRoutes: identificação/controle dos PCs do laboratório
// (hour-timer-app). Heartbeat é público (device_uuid local é a identidade,
// sem login); listar/renomear/despairar/mandar aviso é admin-only.
func (s *Server) registerLabDeviceRoutes(mux *http.ServeMux) {
	const min = time.Minute
	mux.HandleFunc("GET /hour-lab-devices", s.adminGuard(s.handleListLabDevices))
	mux.HandleFunc("POST /hour-lab-devices/pair", s.rateLimit(30, min, s.adminGuard(s.handlePairLabDevice)))
	mux.HandleFunc("PATCH /hour-lab-devices/{id}", s.rateLimit(30, min, s.adminGuard(s.handleRenameLabDevice)))
	mux.HandleFunc("DELETE /hour-lab-devices/{id}", s.rateLimit(30, min, s.adminGuard(s.handleDeleteLabDevice)))
	mux.HandleFunc("POST /hour-lab-devices/{id}/unpair", s.rateLimit(30, min, s.adminGuard(s.handleUnpairLabDevice)))
	mux.HandleFunc("POST /hour-lab-devices/{id}/message", s.rateLimit(30, min, s.adminGuard(s.handleSendLabDeviceMessage)))
	mux.HandleFunc("POST /hour-lab-devices/{id}/reset-secret", s.rateLimit(30, min, s.adminGuard(s.handleResetLabDeviceSecret)))
	mux.HandleFunc("GET /hour-lab-devices/{id}/programs", s.adminGuard(s.handleGetLabDevicePrograms))
	// Captura de tela sob demanda: pedir é admin, a imagem chega pelo próprio
	// PC (rota pública abaixo) e o histórico fica com quem pediu registrado.
	mux.HandleFunc("POST /hour-lab-devices/{id}/screenshot", s.rateLimit(30, min, s.adminGuard(s.handleRequestLabDeviceScreenshot)))
	mux.HandleFunc("GET /hour-lab-devices/{id}/screenshots", s.adminGuard(s.handleListLabDeviceScreenshots))
	mux.HandleFunc("DELETE /hour-lab-devices/{id}/screenshots/{shotId}", s.rateLimit(60, min, s.adminGuard(s.handleDeleteLabDeviceScreenshot)))
	// Imagem do ícone por hash de conteúdo — admin-only como o resto do
	// domínio; a resposta é cacheável pra sempre (a URL é o próprio conteúdo).
	mux.HandleFunc("GET /program-icons/{hash}", s.adminGuard(s.handleLabProgramIcon))

	// Programas esperados nos PCs do lab (cadastro do admin) — o cruzamento com
	// o inventário de cada PC sai em /hour-lab-devices/{id}/programs.
	mux.HandleFunc("GET /hour-lab-expected-programs", s.adminGuard(s.handleListExpectedPrograms))
	mux.HandleFunc("POST /hour-lab-expected-programs", s.rateLimit(30, min, s.adminGuard(s.handleCreateExpectedProgram)))
	mux.HandleFunc("PATCH /hour-lab-expected-programs/{id}", s.rateLimit(30, min, s.adminGuard(s.handleUpdateExpectedProgram)))
	mux.HandleFunc("DELETE /hour-lab-expected-programs/{id}", s.rateLimit(30, min, s.adminGuard(s.handleDeleteExpectedProgram)))

	// Heartbeat a cada ~30s por PC — limite folgado pra cobrir reconexões/retries.
	mux.HandleFunc("POST /public/lab-devices/heartbeat", s.rateLimit(120, min, s.handleLabDeviceHeartbeat))
	// Inventário é raro (muda quando alguém instala algo): o app manda no boot e
	// uma vez por dia, então 10/min por IP já cobre reinstalação e retry.
	mux.HandleFunc("POST /public/lab-devices/inventory", s.rateLimit(10, min, s.handleLabDeviceInventory))
	// Segundo passo da coleta (só os ícones que o servidor ainda não tem) —
	// mesmo orçamento do inventário, já que sempre vem logo depois dele.
	mux.HandleFunc("POST /public/lab-devices/icons", s.rateLimit(10, min, s.handleLabDeviceIcons))
	// Imagem da captura — só entra com pedido recente do admin (a rota é
	// pública, autenticada pelo segredo do dispositivo).
	mux.HandleFunc("POST /public/lab-devices/screenshot", s.rateLimit(10, min, s.handleLabDeviceScreenshot))
}

// registerDownloadsRoutes: catálogo de downloads do Santos Hub. Cadastro/edição/
// remoção são admin-only (mesmo critério de model3d — biblioteca gerida à mão,
// sem cargo personalizado dedicado); /public/downloads alimenta o app Tauri
// instalado nos PCs da empresa — sem login de usuário, mas exigindo o PAT do
// Hub (santosHubGuard): o catálogo entrega instaladores .exe/.msi/.ps1/.bat.
// /public/downloads/hub-installer é a exceção genuinamente pública: alimenta a
// landing page web (dashboard/web:/hub), que não pode guardar o PAT do Hub em
// JS de cliente — ver handleGetPublicHubInstaller.
func (s *Server) registerDownloadsRoutes(mux *http.ServeMux) {
	const min = time.Minute
	mux.HandleFunc("GET /auth/admin/downloads", s.adminGuard(s.handleListDownloadsAdmin))
	mux.HandleFunc("POST /auth/admin/downloads/presign", s.rateLimit(20, min, s.adminGuard(s.handleDownloadsPresign)))
	mux.HandleFunc("POST /auth/admin/downloads", s.rateLimit(20, min, s.adminGuard(s.handleCreateDownload)))
	mux.HandleFunc("PATCH /auth/admin/downloads/{id}", s.rateLimit(30, min, s.adminGuard(s.handleUpdateDownload)))
	mux.HandleFunc("DELETE /auth/admin/downloads/{id}", s.adminGuard(s.handleDeleteDownload))

	mux.HandleFunc("GET /public/downloads", s.rateLimit(120, min, s.santosHubGuard(s.handleListPublicDownloads)))
	mux.HandleFunc("GET /public/downloads/hub-installer", s.rateLimit(120, min, s.handleGetPublicHubInstaller))
}

func (s *Server) registerInstagramRoutes(mux *http.ServeMux) {
	const min = time.Minute

	// Webhook — chamado pela Meta, não por um usuário logado. Autenticado por
	// hub.verify_token (handshake GET) / assinatura HMAC (evento POST), nunca
	// por cookie/PAT — por isso sem authGuard/adminGuard aqui.
	mux.HandleFunc("GET /webhooks/instagram", s.rateLimit(30, min, s.handleInstagramWebhookVerify))
	mux.HandleFunc("POST /webhooks/instagram", s.rateLimit(120, min, s.handleInstagramWebhookEvent))

	// Publicações recentes da conta — alimenta o seletor visual do mapeamento.
	mux.HandleFunc("GET /instagram/media", s.rateLimit(30, min, s.adminGuard(s.handleListInstagramMedia)))

	// Mapeamento publicação -> link de destino (admin-only; tabela pequena,
	// gerida manualmente — não justifica um cargo personalizado dedicado).
	mux.HandleFunc("GET /instagram/links", s.adminGuard(s.handleListInstagramCommentLinks))
	mux.HandleFunc("PUT /instagram/links/{mediaId}", s.rateLimit(30, min, s.adminGuard(s.handleUpsertInstagramCommentLink)))
	mux.HandleFunc("DELETE /instagram/links/{mediaId}", s.adminGuard(s.handleDeleteInstagramCommentLink))
}

// registerDriveRoutes: pastas de arquivos vinculadas ao Google Drive. CRUD de
// pasta e configuração de ACL são admin-only (só admin decide quem vê o quê);
// leitura/listagem/download/upload de arquivo usam folderAccessGuard, que
// resolve o acesso EFETIVO daquela pasta (cargo fixo/personalizado ou membro
// individual — ver drive_access.go), não um resource fixo de cargo.
func (s *Server) registerDriveRoutes(mux *http.ServeMux) {
	const min = time.Minute

	mux.HandleFunc("GET /auth/admin/drive-folders/browse", s.rateLimit(30, min, s.adminGuard(s.handleBrowseDriveFolders)))
	mux.HandleFunc("GET /auth/admin/drive-folders", s.adminGuard(s.handleListDriveFoldersAdmin))
	mux.HandleFunc("POST /auth/admin/drive-folders", s.rateLimit(20, min, s.adminGuard(s.handleCreateDriveFolder)))
	mux.HandleFunc("PUT /auth/admin/drive-folders/{id}", s.rateLimit(30, min, s.adminGuard(s.handleUpdateDriveFolder)))
	mux.HandleFunc("DELETE /auth/admin/drive-folders/{id}", s.adminGuard(s.sudoGuard(s.handleDeleteDriveFolder)))
	mux.HandleFunc("GET /auth/admin/drive-folders/{id}/access", s.adminGuard(s.handleGetDriveFolderAccess))
	mux.HandleFunc("PUT /auth/admin/drive-folders/{id}/access", s.rateLimit(30, min, s.adminGuard(s.handleSetDriveFolderAccess)))

	mux.HandleFunc("GET /drive-folders/mine", s.authGuard(s.handleListMyDriveFolders))
	// list e download não tinham rate limit — como a service account é
	// compartilhada por TODAS as pastas/usuários, um único usuário martelando
	// essas rotas sem limite podia estourar a cota do Drive pro ecossistema
	// inteiro, não só pra pasta dele.
	mux.HandleFunc("GET /drive-folders/{id}/files", s.rateLimit(60, min, s.folderAccessGuard("read", s.handleListDriveFolderFiles)))
	mux.HandleFunc("GET /drive-folders/{id}/files/{fileId}/download", s.rateLimit(60, min, s.folderAccessGuard("read", s.handleDownloadDriveFile)))
	mux.HandleFunc("GET /drive-folders/{id}/files/{fileId}/thumbnail", s.rateLimit(120, min, s.folderAccessGuard("read", s.handleDriveFileThumbnail)))
	mux.HandleFunc("PATCH /drive-folders/{id}/files/{fileId}", s.rateLimit(30, min, s.folderAccessGuard("write", s.handleRenameDriveFile)))
	mux.HandleFunc("DELETE /drive-folders/{id}/files/{fileId}", s.rateLimit(30, min, s.folderAccessGuard("write", s.handleDeleteDriveFile)))
	mux.HandleFunc("PATCH /drive-folders/{id}/files/{fileId}/move", s.rateLimit(30, min, s.folderAccessGuard("write", s.handleMoveDriveFile)))
	mux.HandleFunc("POST /drive-folders/{id}/files", s.rateLimit(10, min, s.folderAccessGuard("write", s.handleUploadDriveFile)))
	mux.HandleFunc("POST /drive-folders/{id}/folders", s.rateLimit(20, min, s.folderAccessGuard("write", s.handleCreateDriveSubfolder)))
}
