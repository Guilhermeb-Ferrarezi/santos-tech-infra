package main

import (
	"os"
	"strings"
)

type Config struct {
	Port          string
	AuthBaseURL   string // raiz da API central (rotas /auth, /status, /llms.txt)
	ClaudeBaseURL string // raiz do claude agent (rotas /claude/*)
	EmailAPIURL   string // ex: https://mails.santos-tech.com/api
	OpenAPIPath   string // caminho do docs/openapi.yaml (resource santos-tech://openapi/auth)
	Production    bool
}

func LoadConfig() Config {
	return Config{
		Port:          getEnv("PORT", "3335"),
		AuthBaseURL:   strings.TrimRight(getEnv("AUTH_API_URL", "https://api.santos-tech.com"), "/"),
		ClaudeBaseURL: strings.TrimRight(getEnv("CLAUDE_API_URL", "https://api.santos-tech.com"), "/"),
		EmailAPIURL:   strings.TrimRight(getEnv("EMAIL_API_URL", "https://mails.santos-tech.com/api"), "/"),
		OpenAPIPath:   getEnv("OPENAPI_PATH", "../../docs/openapi.yaml"),
		Production:    getEnv("NODE_ENV", "development") == "production",
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
