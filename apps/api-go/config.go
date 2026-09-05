package main

import (
	"log/slog"
	"os"
	"strings"
	"time"
)

type Config struct {
	Port        string
	DatabaseURL string
	// PortalDatabaseURL: banco do domínio do portal (tabelas course/badge/goals/
	// "user"/logs…). Hoje separado do auth (db santos-tech só tem users). Quando
	// tudo for consolidado num banco só, basta NÃO definir PORTAL_DATABASE_URL
	// que ele cai no DatabaseURL e os dois pools apontam pro mesmo lugar.
	PortalDatabaseURL  string
	RedisURL           string
	JWTSecret          string
	JWTRefreshSecret   string
	CookieDomain       string
	PublicOrigin       string // origem pública desta API (issuer OAuth, metadata)
	CORSOrigins        []string
	AuthWebOrigin      string
	DashboardWebOrigin string // origem pública do dashboard (santos-tech.com/dashboard) — usada pra montar links públicos (ex.: /sessao/{token})
	GoogleClientID     string
	GoogleClientSecret string
	GoogleCallbackURL  string
	EmailAPIURL        string // ex: https://mails.santos-tech.com/api
	EmailAPIKey        string
	AgentURL           string // base do claude agent (health em /claude/health)
	SocialAlertEmail   string // email para notificar quando post vai para revisão
	Production         bool

	// OAuthAudEnforce (OAUTH_AUD_ENFORCE=1): recusa, nas rotas de sessão do
	// painel (authGuard e derivados), access tokens emitidos pelo /oauth/token —
	// reconhecidos pelo claim aud=<client_id>. Default DESLIGADO: os tokens já
	// saem marcados, mas ligar a recusa quebra clients OAuth e o app mobile que
	// hoje usam o token do /oauth/token como sessão completa. Ligue só depois de
	// migrar todos eles pro /oauth/userinfo.
	OAuthAudEnforce bool

	// Gateway de notificações do portal do aluno (templates/dispatches). Vazio =
	// rotas de template/dispatch respondem 502 "gateway não configurado".
	NotificationsGatewayURL   string // ex: https://portal.santos-tech.com (base, sem barra final)
	NotificationsSharedSecret string // header x-notification-admin-secret

	// Loki (tela de Logs, admin-only). Vazio = endpoints respondem 503.
	// Em produção, alcançável pela rede docker da Coolify (ex: http://obs-loki:3100).
	LokiURL string

	// Sentry (tela de Erros, admin-only). Vazio = endpoints respondem 503.
	// Token de API da org (Settings → Auth Tokens, escopo project:read/org:read).
	SentryOrgSlug string
	SentryToken   string

	// PostHog (tela de Analytics, admin-only). Vazio = endpoints respondem 503.
	// Personal API Key (Settings → Personal API Keys, escopo query:read) — NUNCA
	// o Project API Key (esse é público, vai embutido no client dos 3 sites).
	PostHogAPIKey    string
	PostHogProjectID string
	PostHogHost      string // base da API, ex: https://us.posthog.com (sem barra final)

	// Cloudflare R2 (S3-compatível) para uploads (ex: avatares). Vazio = desabilitado.
	R2AccountID string
	R2AccessKey string
	R2SecretKey string
	R2Bucket    string
	R2PublicURL string // base pública (CDN), ex: https://cdn.santos-tech.com

	// Santos Hub (app Tauri dos PCs da empresa): PAT embarcado no build do app,
	// exigido em GET /public/downloads. VAZIO = a rota responde 503 — o
	// catálogo lista instaladores .exe/.msi/.ps1/.bat e não pode ficar aberto
	// à internet por falta de configuração (fail-closed de propósito).
	SantosHubToken string

	// FleetAdminSSHPublicKeys: chave(s) pública(s) do(s) admin(s) da frota
	// (Guilherme), devolvidas no heartbeat do hour-timer-app pra ele instalar
	// em ~/.ssh/authorized_keys de cada PC de laboratório — é assim que dá
	// pra entrar via SSH DE FORA pra dentro do PC (o sshPublicKey que o PC
	// manda É a identidade do próprio PC, não abre acesso nenhum; ver
	// handlers_lab_devices.go). VAZIO = nenhuma chave é instalada, sem quebrar
	// nada — o heartbeat simplesmente não devolve o campo.
	FleetAdminSSHPublicKeys []string

	// Arquivos (pastas de admin vinculadas ao Google Drive, ver drive.go): JSON
	// da service account em base64. Vazio = feature desabilitada, rotas de
	// /drive-folders respondem 503. O admin compartilha cada pasta manualmente
	// com o e-mail da service account no próprio Google Drive.
	GoogleDriveSAJSONB64 string

	// Upload no Drive (ver UploadFile em drive.go): service accounts do
	// Google não têm cota de armazenamento própria (só em Shared Drives, que
	// exige Workspace) — todo POST de arquivo novo falha com 403
	// storageQuotaExceeded. Como não temos Workspace, o upload roda com um
	// token OAuth de uma conta Google real (com plano de armazenamento
	// pago), obtido uma vez via authorization code flow e renovado por este
	// refresh token. Vazio = upload cai no client da service account mesmo
	// (mantém o 403 de antes); leitura/listagem/download não são afetados,
	// eles não consomem cota.
	GoogleDriveOAuthClientID     string
	GoogleDriveOAuthClientSecret string
	GoogleDriveOAuthRefreshToken string

	// Automação de resposta a comentário do Instagram (private reply via
	// Graph API — substitui o ManyChat). Vazio (AppSecret ou AccessToken) =
	// webhook desabilitado (responde 503 em vez de processar sem validar
	// assinatura). Ver instagram.go / instagram_client.go.
	InstagramAppSecret          string // App secret Meta — valida X-Hub-Signature-256
	InstagramAccessToken        string // token de acesso (longa duração) da conta @escolasantostech
	InstagramUserID             string // ID numérico da conta IG business
	InstagramWebhookVerifyToken string // hub.verify_token do handshake de assinatura do webhook

	// Publicação automática na Página do Facebook (ver social_publish.go).
	// FacebookAccessToken é um Page Access Token (não expira sozinho, só se
	// revogado — obtido trocando um User Token de longa duração via
	// /me/accounts). Vazio (Token ou PageID) = adaptador do Facebook
	// desabilitado, plataforma cai de volta pro checklist manual.
	FacebookAccessToken string
	FacebookPageID      string

	// Localização automática (Instagram location_id / Facebook place) NÃO é
	// env var — é config fixa no banco (social_settings), editável pelo
	// admin em Configurações do Calendário Editorial (GET/PUT /social/settings,
	// ver social.go/handlers_social.go). Cresceria sem redeploy toda vez que
	// alguém quisesse trocar o local, então não faz sentido como env.

	// Roteador de chaves de API (failover automático em 401/sem-créditos de
	// provedores externos). Deriva a chave AES-256 que cifra as chaves antes de
	// persistir. Vazio = feature desabilitada, endpoints respondem 503.
	VaultSecret string
	// VaultSalt: salt do HKDF que deriva a chave do cofre (API_VAULT_SALT).
	// Vazio cai num default fixo. ATENÇÃO: trocar o salt depois de cadastrar
	// chaves torna ilegível tudo que foi cifrado no formato v2.
	VaultSalt string

	// Web Push (notificações do navegador — nova tarefa, novo email). Par de
	// chaves VAPID (gerado uma vez, ex. `npx web-push generate-vapid-keys`).
	// Vazio (Public ou Private) = feature desabilitada, endpoints respondem 503.
	VAPIDPublicKey  string
	VAPIDPrivateKey string
	VAPIDSubject    string // contato do remetente, ex. "mailto:contato@santos-tech.com"

	// Segredo compartilhado do webhook POST /webhooks/email/new, chamado pelo
	// docker-mailserver (Sieve pipe) quando chega um email novo. Vazio =
	// webhook sempre rejeita (fail-closed), mesmo padrão do InstagramAppSecret.
	EmailWebhookSecret string

	// Sync automático do roteador com o scanner de secrets vazados
	// (secrets-go): a cada SecretsSyncInterval, importa os hits confirmados
	// ativos e cadastra no roteador (provider automático por provedor).
	// Habilitado quando SecretsSyncToken está setado E VaultSecret também
	// (sem o vault não há como cifrar as chaves antes de persistir).
	SecretsSyncURL      string // base, ex: https://api.santos-tech.com/secrets
	SecretsSyncToken    string // mesmo valor de INTERNAL_SYNC_TOKEN no secrets-go
	SecretsSyncInterval time.Duration
}

func LoadConfig() Config {
	c := Config{
		Port:               getEnv("PORT", "3333"),
		DatabaseURL:        mustEnv("DATABASE_URL"),
		PortalDatabaseURL:  getEnv("PORTAL_DATABASE_URL", os.Getenv("DATABASE_URL")),
		RedisURL:           mustEnv("REDIS_URL"),
		JWTSecret:          mustEnv("JWT_SECRET"),
		JWTRefreshSecret:   mustEnv("JWT_REFRESH_SECRET"),
		CookieDomain:       getEnv("COOKIE_DOMAIN", ""),
		PublicOrigin:       strings.TrimRight(getEnv("PUBLIC_ORIGIN", "https://api.santos-tech.com"), "/"),
		CORSOrigins:        splitCSV(getEnv("CORS_ORIGIN", "")),
		AuthWebOrigin:      getEnv("AUTH_WEB_ORIGIN", ""),
		DashboardWebOrigin: strings.TrimRight(getEnv("DASHBOARD_WEB_ORIGIN", "https://santos-tech.com/dashboard"), "/"),
		GoogleClientID:     getEnv("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret: getEnv("GOOGLE_CLIENT_SECRET", ""),
		GoogleCallbackURL:  getEnv("GOOGLE_CALLBACK_URL", ""),
		EmailAPIURL:        strings.TrimRight(getEnv("EMAIL_API_URL", "https://mails.santos-tech.com/api"), "/"),
		AgentURL:           strings.TrimRight(getEnv("AGENT_URL", "https://api.santos-tech.com"), "/"),
		SocialAlertEmail:   getEnv("SOCIAL_ALERT_EMAIL", ""),
		EmailAPIKey:        mustEnv("EMAIL_API_KEY"),
		Production:         getEnv("NODE_ENV", "development") == "production",
		OAuthAudEnforce:    getEnv("OAUTH_AUD_ENFORCE", "") == "1",

		NotificationsGatewayURL:   strings.TrimRight(getEnv("NOTIFICATIONS_PORTAL_API_URL", ""), "/"),
		NotificationsSharedSecret: getEnv("NOTIFICATIONS_SHARED_SECRET", ""),

		LokiURL: strings.TrimRight(getEnv("LOKI_URL", ""), "/"),

		SentryOrgSlug: getEnv("SENTRY_ORG_SLUG", ""),
		SentryToken:   getEnv("SENTRY_API_TOKEN", ""),

		PostHogAPIKey:    getEnv("POSTHOG_PERSONAL_API_KEY", ""),
		PostHogProjectID: getEnv("POSTHOG_PROJECT_ID", ""),
		PostHogHost:      strings.TrimRight(getEnv("POSTHOG_HOST", "https://us.posthog.com"), "/"),

		R2AccountID: getEnv("CF_ACCOUNT_ID", ""),
		R2AccessKey: getEnv("CF_R2_ACCESS_KEY", ""),
		R2SecretKey: getEnv("CF_R2_SECRET_KEY", ""),
		R2Bucket:    getEnv("CF_R2_BUCKET_NAME", ""),
		R2PublicURL: strings.TrimRight(getEnv("CF_R2_PUBLIC_URL", ""), "/"),

		SantosHubToken: getEnv("SANTOS_HUB_TOKEN", ""),

		FleetAdminSSHPublicKeys: splitCSV(getEnv("FLEET_ADMIN_SSH_PUBLIC_KEYS", "")),

		GoogleDriveSAJSONB64: getEnv("GOOGLE_DRIVE_SA_JSON_B64", ""),

		GoogleDriveOAuthClientID:     getEnv("GOOGLE_DRIVE_OAUTH_CLIENT_ID", ""),
		GoogleDriveOAuthClientSecret: getEnv("GOOGLE_DRIVE_OAUTH_CLIENT_SECRET", ""),
		GoogleDriveOAuthRefreshToken: getEnv("GOOGLE_DRIVE_OAUTH_REFRESH_TOKEN", ""),

		InstagramAppSecret:          getEnv("INSTAGRAM_APP_SECRET", ""),
		InstagramAccessToken:        getEnv("INSTAGRAM_ACCESS_TOKEN", ""),
		InstagramUserID:             getEnv("INSTAGRAM_USER_ID", ""),
		InstagramWebhookVerifyToken: getEnv("INSTAGRAM_WEBHOOK_VERIFY_TOKEN", ""),

		FacebookAccessToken: getEnv("FACEBOOK_ACCESS_TOKEN", ""),
		FacebookPageID:      getEnv("FACEBOOK_PAGE_ID", ""),

		VaultSecret: getEnv("API_VAULT_SECRET", ""),
		VaultSalt:   getEnv("API_VAULT_SALT", ""),

		VAPIDPublicKey:  getEnv("VAPID_PUBLIC_KEY", ""),
		VAPIDPrivateKey: getEnv("VAPID_PRIVATE_KEY", ""),
		VAPIDSubject:    getEnv("VAPID_SUBJECT", ""),

		EmailWebhookSecret: getEnv("EMAIL_WEBHOOK_SECRET", ""),

		SecretsSyncURL:      strings.TrimRight(getEnv("SECRETS_SYNC_URL", ""), "/"),
		SecretsSyncToken:    getEnv("SECRETS_SYNC_TOKEN", ""),
		SecretsSyncInterval: getEnvDuration("SECRETS_SYNC_INTERVAL", time.Hour),
	}
	if c.AuthWebOrigin == "" && len(c.CORSOrigins) > 0 {
		c.AuthWebOrigin = c.CORSOrigins[0]
	}
	// HMAC-SHA256 aceita qualquer tamanho de chave, mas uma chave curta é
	// vulnerável a brute-force: com menos de 256 bits o espaço de chaves cabe
	// num ataque offline. 32 bytes (256 bits) é o mínimo seguro e o tamanho
	// recomendado pela RFC 7518 §3.2 para HS256. Falha no boot, como mustEnv.
	if len(c.JWTSecret) < 32 {
		slog.Error("JWT_SECRET deve ter pelo menos 32 caracteres (256 bits) para uso seguro com HS256",
			"got", len(c.JWTSecret))
		os.Exit(1)
	}
	if len(c.JWTRefreshSecret) < 32 {
		slog.Error("JWT_REFRESH_SECRET deve ter pelo menos 32 caracteres (256 bits) para uso seguro com HS256",
			"got", len(c.JWTRefreshSecret))
		os.Exit(1)
	}
	// JWT_SECRET e JWT_REFRESH_SECRET iguais por erro de config fariam um
	// refresh token (7 dias) validar como access token em qualquer token SEM o
	// claim "typ" — só tokens antigos, emitidos antes da introdução desse claim
	// (ver token.go), mas o boot é o lugar certo pra fechar essa classe de erro
	// de vez, mesmo que o risco prático hoje seja residual.
	if c.JWTSecret == c.JWTRefreshSecret {
		slog.Error("JWT_SECRET e JWT_REFRESH_SECRET não podem ser iguais")
		os.Exit(1)
	}
	// allowedOrigin (server.go) é fail-closed por design: allowlist vazia
	// bloqueia toda origem em rotas com credencial. Sem essa checagem no boot,
	// esquecer CORS_ORIGIN/AUTH_WEB_ORIGIN sobe o processo normalmente e só
	// aparece em produção como login/cookie quebrado pra todo mundo — silencioso
	// até alguém notar. Falha aqui, como os demais campos obrigatórios.
	if len(c.CORSOrigins) == 0 && c.AuthWebOrigin == "" {
		slog.Error("nenhuma origem CORS configurada — defina CORS_ORIGIN e/ou AUTH_WEB_ORIGIN")
		os.Exit(1)
	}
	return c
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("variável de ambiente obrigatória ausente", "key", key)
		os.Exit(1)
	}
	return v
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}
