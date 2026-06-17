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
	BotAPIURL     string // raiz do dashboard API do bot-go (rotas /api/*), ex: https://api.santos-tech.com/bot
	BotDashKey    string // DASH_API_KEY do bot-go (header X-Dash-Key) p/ tools de agendamentos/leads/conversas
	OpenAPIPath   string // caminho do docs/openapi.yaml (resource santos-tech://openapi/auth)
	PublicURL     string // URL pública deste MCP (resource OAuth, ex: https://api.santos-tech.com/mcp)
	Production    bool
}

func LoadConfig() Config {
	return Config{
		Port:          getEnv("PORT", "3335"),
		AuthBaseURL:   strings.TrimRight(getEnv("AUTH_API_URL", "https://api.santos-tech.com"), "/"),
		ClaudeBaseURL: strings.TrimRight(getEnv("CLAUDE_API_URL", "https://api.santos-tech.com"), "/"),
		EmailAPIURL:   strings.TrimRight(getEnv("EMAIL_API_URL", "https://mails.santos-tech.com/api"), "/"),
		BotAPIURL:     strings.TrimRight(getEnv("BOT_API_URL", "https://api.santos-tech.com/bot"), "/"),
		BotDashKey:    getEnv("BOT_DASH_KEY", ""),
		OpenAPIPath:   getEnv("OPENAPI_PATH", "../../docs/openapi.yaml"),
		PublicURL:     strings.TrimRight(getEnv("MCP_PUBLIC_URL", "https://api.santos-tech.com/mcp"), "/"),
		Production:    getEnv("NODE_ENV", "development") == "production",
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
