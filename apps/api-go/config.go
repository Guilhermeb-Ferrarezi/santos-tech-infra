package main

import (
	"log/slog"
	"os"
	"strings"
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
	GoogleClientID     string
	GoogleClientSecret string
	GoogleCallbackURL  string
	EmailAPIURL        string // ex: https://mails.santos-tech.com/api
	EmailAPIKey        string
	AgentURL           string // base do claude agent (health em /claude/health)
	SocialAlertEmail   string // email para notificar quando post vai para revisão
	Production         bool

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

	// Cloudflare R2 (S3-compatível) para uploads (ex: avatares). Vazio = desabilitado.
	R2AccountID string
	R2AccessKey string
	R2SecretKey string
	R2Bucket    string
	R2PublicURL string // base pública (CDN), ex: https://cdn.santos-tech.com

	// Automação de resposta a comentário do Instagram (private reply via
	// Graph API — substitui o ManyChat). Vazio (AppSecret ou AccessToken) =
	// webhook desabilitado (responde 503 em vez de processar sem validar
	// assinatura). Ver instagram.go / instagram_client.go.
	InstagramAppSecret          string // App secret Meta — valida X-Hub-Signature-256
	InstagramAccessToken        string // token de acesso (longa duração) da conta @escolasantostech
	InstagramUserID             string // ID numérico da conta IG business
	InstagramWebhookVerifyToken string // hub.verify_token do handshake de assinatura do webhook
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
		GoogleClientID:     getEnv("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret: getEnv("GOOGLE_CLIENT_SECRET", ""),
		GoogleCallbackURL:  getEnv("GOOGLE_CALLBACK_URL", ""),
		EmailAPIURL:        strings.TrimRight(getEnv("EMAIL_API_URL", "https://mails.santos-tech.com/api"), "/"),
		AgentURL:           strings.TrimRight(getEnv("AGENT_URL", "https://api.santos-tech.com"), "/"),
		SocialAlertEmail:   getEnv("SOCIAL_ALERT_EMAIL", ""),
		EmailAPIKey:        mustEnv("EMAIL_API_KEY"),
		Production:         getEnv("NODE_ENV", "development") == "production",

		NotificationsGatewayURL:   strings.TrimRight(getEnv("NOTIFICATIONS_PORTAL_API_URL", ""), "/"),
		NotificationsSharedSecret: getEnv("NOTIFICATIONS_SHARED_SECRET", ""),

		LokiURL: strings.TrimRight(getEnv("LOKI_URL", ""), "/"),

		SentryOrgSlug: getEnv("SENTRY_ORG_SLUG", ""),
		SentryToken:   getEnv("SENTRY_API_TOKEN", ""),

		R2AccountID: getEnv("CF_ACCOUNT_ID", ""),
		R2AccessKey: getEnv("CF_R2_ACCESS_KEY", ""),
		R2SecretKey: getEnv("CF_R2_SECRET_KEY", ""),
		R2Bucket:    getEnv("CF_R2_BUCKET_NAME", ""),
		R2PublicURL: strings.TrimRight(getEnv("CF_R2_PUBLIC_URL", ""), "/"),

		InstagramAppSecret:          getEnv("INSTAGRAM_APP_SECRET", ""),
		InstagramAccessToken:        getEnv("INSTAGRAM_ACCESS_TOKEN", ""),
		InstagramUserID:             getEnv("INSTAGRAM_USER_ID", ""),
		InstagramWebhookVerifyToken: getEnv("INSTAGRAM_WEBHOOK_VERIFY_TOKEN", ""),
	}
	if c.AuthWebOrigin == "" && len(c.CORSOrigins) > 0 {
		c.AuthWebOrigin = c.CORSOrigins[0]
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
