package main

import (
	"log/slog"
	"os"
	"strings"
)

type Config struct {
	Port             string
	DatabaseURL      string
	RedisURL         string
	JWTSecret        string
	CORSOrigins      []string
	EmailAPIURL      string
	EmailAPIKey      string
	NotifyEmail      string // destino do aviso de pagamento confirmado (opcional)
	EFIBaseURL       string
	EFIClientID      string
	EFIClientSecret  string
	EFICertP12Base64 string
	EFICertPassword  string
	EFIPixKey        string
	EFIWebhookSecret string
	EFIWebhookURL    string // URL pública a registrar no webhook da Efí (sem ?hmac)
	Production       bool
}

func LoadConfig() Config {
	return Config{
		Port:             getEnv("PORT", "3336"),
		DatabaseURL:      mustEnv("DATABASE_URL"),
		RedisURL:         mustEnv("REDIS_URL"),
		JWTSecret:        mustEnv("JWT_SECRET"),
		CORSOrigins:      splitCSV(getEnv("CORS_ORIGIN", "")),
		EmailAPIURL:      strings.TrimRight(getEnv("EMAIL_API_URL", "https://mails.santos-tech.com/api"), "/"),
		EmailAPIKey:      getEnv("EMAIL_API_KEY", ""),
		NotifyEmail:      strings.TrimSpace(getEnv("PAYMENT_NOTIFY_EMAIL", "")),
		EFIBaseURL:       strings.TrimRight(getEnv("EFI_BASE_URL", "https://pix-h.api.efipay.com.br"), "/"),
		EFIClientID:      mustEnv("EFI_CLIENT_ID"),
		EFIClientSecret:  mustEnv("EFI_CLIENT_SECRET"),
		EFICertP12Base64: mustEnv("EFI_CERT_P12_BASE64"),
		EFICertPassword:  getEnv("EFI_CERT_PASSWORD", ""),
		EFIPixKey:        mustEnv("EFI_PIX_KEY"),
		EFIWebhookSecret: getEnv("EFI_WEBHOOK_SECRET", ""),
		EFIWebhookURL:    strings.TrimRight(getEnv("EFI_WEBHOOK_URL", ""), "/"),
		Production:       getEnv("NODE_ENV", "development") == "production",
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
