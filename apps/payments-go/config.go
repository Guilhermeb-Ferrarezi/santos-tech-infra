package main

import (
	"log/slog"
	"os"
	"strings"
)

type Config struct {
	Port               string
	DatabaseURL        string
	RedisURL           string
	JWTSecret          string
	CORSOrigins        []string
	EmailAPIURL        string
	EmailAPIKey        string
	DotfyBaseURL       string
	DotfyAPIKey        string
	DotfyWebhookSecret string
	DotfyWebhookSigHdr string // header HTTP que carrega a assinatura do webhook
	Production         bool
}

func LoadConfig() Config {
	return Config{
		Port:               getEnv("PORT", "3336"),
		DatabaseURL:        mustEnv("DATABASE_URL"),
		RedisURL:           mustEnv("REDIS_URL"),
		JWTSecret:          mustEnv("JWT_SECRET"),
		CORSOrigins:        splitCSV(getEnv("CORS_ORIGIN", "")),
		EmailAPIURL:        strings.TrimRight(getEnv("EMAIL_API_URL", "https://mails.santos-tech.com/api"), "/"),
		EmailAPIKey:        getEnv("EMAIL_API_KEY", ""),
		DotfyBaseURL:       strings.TrimRight(getEnv("DOTFY_BASE_URL", "https://app.dotfy.com.br"), "/"),
		DotfyAPIKey:        mustEnv("DOTFY_API_KEY"),
		DotfyWebhookSecret: getEnv("DOTFY_WEBHOOK_SECRET", ""),
		DotfyWebhookSigHdr: getEnv("DOTFY_WEBHOOK_SIG_HEADER", "X-Signature"),
		Production:         getEnv("NODE_ENV", "development") == "production",
	}
}

func getEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		slog.Error("variável de ambiente obrigatória ausente", "key", k)
		os.Exit(1)
	}
	return v
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
