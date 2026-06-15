package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

type Config struct {
	Port        string
	BasePath    string
	DatabaseURL string
	RedisURL    string

	// Tenant único (single-tenant)
	TenantID string

	// Meta Cloud API (WhatsApp)
	MetaAppSecret          string
	MetaWebhookVerifyToken string
	MetaPhoneNumberID      string
	MetaAccessToken        string

	// Agent-go (LLM)
	AgentGoURL    string
	AgentGoSecret string

	// Site oficial — fonte das rotas que o bot pode consultar (via sitemap.xml).
	SiteURL string

	// Notion — agendamento de aulas (token de integração escopado + base de agenda).
	// NotionExperimentalDSID: data source ("Agenda — Aulas Experimentais"). O bot lê
	// os horários ocupados e grava os agendamentos AQUI — separado da agenda de aulas
	// regulares, pra não confundir com quem já é aluno.
	NotionToken            string
	NotionExperimentalDSID string

	// Evolution API — captura de leads do número não-oficial (webhook).
	EvolutionWebhookSecret string
	EvolutionAPIURL        string
	EvolutionAPIKey        string
	EvolutionInstance      string

	// Notificações admin
	AdminWhatsAppNumber string // E.164, ex: 5516991445664

	// Worker
	OutboxBatchSize      int
	OutboxIdleIntervalMs int
	OutboxMaxAttempts    int
	FollowUpConcurrency  int

	// Follow-up templates Meta (fora da janela 24h)
	FollowUpHasApprovedTemplates bool
	FollowUpTemplateName         string
	FollowUpTemplateLanguage     string

	Production bool

	// Dashboard
	DashAPIKey     string
	DashCORSOrigin string

	// Voz (STT/TTS via OpenAI). VOICE_ENABLED liga a feature.
	VoiceEnabled   bool
	OpenAIKey      string
	OpenAIBaseURL  string
	OpenAITTSVoice string
	OpenAITTSModel string
	OpenAISTTModel string
}

func LoadConfig() Config {
	return Config{
		Port:        getEnv("PORT", "3000"),
		BasePath:    getEnv("BASE_PATH", ""),
		DatabaseURL: mustEnv("DATABASE_URL"),
		RedisURL:    mustEnv("REDIS_URL"),
		TenantID:    mustEnv("TENANT_ID"),

		MetaAppSecret:          getEnv("META_APP_SECRET", ""),
		MetaWebhookVerifyToken: mustEnv("META_WEBHOOK_VERIFY_TOKEN"),
		MetaPhoneNumberID:      getEnv("META_PHONE_NUMBER_ID", ""),
		MetaAccessToken:        getEnv("META_ACCESS_TOKEN", ""),

		AgentGoURL:    strings.TrimRight(getEnv("AGENT_GO_URL", "https://api.santos-tech.com"), "/"),
		AgentGoSecret: mustEnv("AGENT_GO_SECRET"),

		SiteURL: strings.TrimRight(getEnv("SITE_URL", "https://santos-tech.com"), "/"),

		NotionToken: getEnv("NOTION_TOKEN", ""),
		// Data source "Agenda — Aulas Experimentais" (database multi-source 37ef7f42…80bc).
		NotionExperimentalDSID: getEnv("NOTION_EXPERIMENTAL_AGENDA_DS_ID", "37ef7f42-1ce5-8044-bcd2-000b3efd7f76"),

		EvolutionWebhookSecret: getEnv("EVOLUTION_WEBHOOK_SECRET", ""),
		EvolutionAPIURL:        strings.TrimRight(getEnv("EVOLUTION_API_URL", ""), "/"),
		EvolutionAPIKey:        getEnv("EVOLUTION_API_KEY", ""),
		EvolutionInstance:      getEnv("EVOLUTION_INSTANCE", ""),

		AdminWhatsAppNumber: getEnv("ADMIN_WHATSAPP_NUMBER", ""),

		OutboxBatchSize:      envInt("OUTBOX_BATCH_SIZE", 50),
		OutboxIdleIntervalMs: envInt("OUTBOX_IDLE_INTERVAL_MS", 500),
		OutboxMaxAttempts:    envInt("OUTBOX_MAX_ATTEMPTS", 5),
		FollowUpConcurrency:  envInt("FOLLOW_UP_CONCURRENCY", 5),

		FollowUpHasApprovedTemplates: getEnv("FOLLOW_UP_HAS_APPROVED_TEMPLATES", "false") == "true",
		FollowUpTemplateName:         getEnv("FOLLOW_UP_TEMPLATE_NAME", ""),
		FollowUpTemplateLanguage:     getEnv("FOLLOW_UP_TEMPLATE_LANGUAGE", "pt_BR"),

		Production: getEnv("NODE_ENV", "development") == "production",

		DashAPIKey:     getEnv("DASH_API_KEY", ""),
		DashCORSOrigin: getEnv("DASH_CORS_ORIGIN", "https://santos-tech.com"),

		VoiceEnabled:   getEnv("VOICE_ENABLED", "false") == "true",
		OpenAIKey:      getEnv("OPENAI_API_KEY", ""),
		OpenAIBaseURL:  strings.TrimRight(getEnv("OPENAI_BASE_URL", "https://api.openai.com/v1"), "/"),
		OpenAITTSVoice: getEnv("OPENAI_TTS_VOICE", "nova"),
		OpenAITTSModel: getEnv("OPENAI_TTS_MODEL", "gpt-4o-mini-tts"),
		OpenAISTTModel: getEnv("OPENAI_STT_MODEL", "whisper-1"),
	}
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

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return fallback
	}
	return n
}
