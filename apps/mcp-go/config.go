package main

import (
	"os"
	"strings"
)

type Config struct {
	Port           string
	AuthBaseURL    string // raiz da API central (rotas /auth, /status, /llms.txt)
	AuthMeURL      string // /auth/me usado pelo requireAuth p/ validar o token (default: AuthBaseURL + /auth/me)
	ClaudeBaseURL  string // raiz do claude agent (rotas /claude/*)
	EmailAPIURL    string // ex: https://mails.santos-tech.com/api
	BotAPIURL      string // raiz do dashboard API do bot-go (rotas /api/*), ex: https://api.santos-tech.com/bot
	BotDashKey     string // DASH_API_KEY do bot-go (header X-Dash-Key) p/ tools de agendamentos/leads/conversas
	PaymentsAPIURL string // raiz do payments-go (rotas /charges, /stats...), ex: https://api.santos-tech.com/payments
	OpenAPIPath    string // caminho do docs/openapi.yaml (resource santos-tech://openapi/auth)
	PublicURL      string // URL pública deste MCP (resource OAuth, ex: https://api.santos-tech.com/mcp)
	Production     bool
	RedisURL       string // opcional; se vazio, ban check é desabilitado

	// Bambu Lab (impressora 3D via broker de nuvem us.mqtt.bambulab.com).
	// Vazios = tool bambu_status desabilitada. Ver apps/mcp-go/tools_printer.go.
	BambuUserID      string // uid da conta Bambu (design-user-service/my/preference)
	BambuAccessToken string
	BambuDeviceID    string // serial da impressora
}

func LoadConfig() Config {
	return Config{
		Port:           getEnv("PORT", "3335"),
		AuthBaseURL:    strings.TrimRight(getEnv("AUTH_API_URL", "https://api.santos-tech.com"), "/"),
		AuthMeURL:      strings.TrimRight(getEnv("AUTH_ME_URL", ""), "/"),
		ClaudeBaseURL:  strings.TrimRight(getEnv("CLAUDE_API_URL", "https://api.santos-tech.com"), "/"),
		EmailAPIURL:    strings.TrimRight(getEnv("EMAIL_API_URL", "https://mails.santos-tech.com/api"), "/"),
		BotAPIURL:      strings.TrimRight(getEnv("BOT_API_URL", "https://api.santos-tech.com/bot"), "/"),
		BotDashKey:     getEnv("BOT_DASH_KEY", ""),
		PaymentsAPIURL: strings.TrimRight(getEnv("PAYMENTS_API_URL", "https://api.santos-tech.com/payments"), "/"),
		OpenAPIPath:    getEnv("OPENAPI_PATH", "../../docs/openapi.yaml"),
		PublicURL:      strings.TrimRight(getEnv("MCP_PUBLIC_URL", "https://api.santos-tech.com/mcp"), "/"),
		Production:     getEnv("NODE_ENV", "development") == "production",
		RedisURL:       getEnv("REDIS_URL", ""),

		BambuUserID:      getEnv("BAMBU_USER_ID", ""),
		BambuAccessToken: getEnv("BAMBU_ACCESS_TOKEN", ""),
		BambuDeviceID:    getEnv("BAMBU_DEVICE_ID", ""),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
