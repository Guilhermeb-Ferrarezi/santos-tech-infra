package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
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
	AgentGoURL string
	// BotModel — qual Claude responde o cliente ("sonnet" | "opus" | "haiku").
	BotModel      string
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

	// Pool de processamento em background dos webhooks (paralelismo + shutdown).
	BGPoolSlots       int
	BGShutdownTimeout time.Duration

	// Worker
	OutboxBatchSize      int
	OutboxIdleIntervalMs int
	OutboxMaxAttempts    int
	// OutboxClaimLease: por quanto tempo um evento drenado fica reservado para a
	// réplica que o pegou. Vencido, volta à fila (cobre réplica morta no meio).
	OutboxClaimLease    time.Duration
	FollowUpConcurrency int

	// Redis Stream de retries de webhook (reprocesso quase em tempo real).
	// O polling do Postgres continua como safety net, mas com intervalo maior
	// (RetryPollInterval) já que o stream cobre o caminho quente.
	RetryStreamKey      string
	RetryStreamGroup    string
	RetryStreamConsumer string
	RetryPollInterval   time.Duration

	// Cache do debounce_ms por tenant (Redis). Fail-open p/ o banco.
	DebounceCacheTTL time.Duration

	// Follow-up templates Meta (fora da janela 24h)
	FollowUpHasApprovedTemplates bool
	FollowUpTemplateName         string
	FollowUpTemplateLanguage     string

	Production bool

	// Rate limit do caminho de entrada (Redis). Cada mensagem recebida vira uma
	// chamada ao LLM; sem teto, o webhook é um gerador de custo.
	RateLimitEnabled bool
	RatePhoneBurst   int
	RatePhonePerMin  int
	RateGlobalBurst  int
	RateGlobalPerMin int
	LLMDailyMax      int

	// Dashboard
	DashAPIKey     string
	DashCORSOrigin string

	// Auth central — o painel autentica por cookie de sessão (access_token),
	// validado em /auth/me exigindo papel Admin. A DashAPIKey continua valendo
	// para integrações server-to-server.
	AuthMeURL    string
	AuthTimeout  time.Duration
	AuthCacheTTL time.Duration

	// Coolify — webhook de falha de deploy
	CoolifyWebhookSecret string

	// Coolify API — comandos de operação pelo WhatsApp (/status, /redeploy, /logs).
	// Opcionais: se ausentes, os comandos respondem "Coolify não configurada".
	CoolifyAPIURL   string
	CoolifyAPIToken string

	// Sentry — polling periódico de issues novas do ecossistema, notificadas por
	// WhatsApp (mesmo destinatário/liga-desliga do notif_* do Coolify em
	// tenant_config). Vazio = poller desabilitado.
	SentryOrgSlug      string
	SentryAPIToken     string
	SentryPollInterval time.Duration

	// Voz. VOICE_ENABLED é o interruptor geral; o provedor e a voz de cada
	// tenant vêm de tenant_config (painel), com estes valores como fallback.
	// O STT é sempre OpenAI — só o TTS é trocável.
	VoiceEnabled   bool
	OpenAIKey      string
	OpenAIBaseURL  string
	OpenAITTSVoice string
	OpenAITTSModel string
	OpenAISTTModel string

	// ElevenLabs (TTS alternativo — voz clonada). Sem chave, o provider
	// 'elevenlabs' é ignorado e o tenant cai no OpenAI.
	ElevenLabsKey     string
	ElevenLabsBaseURL string
	ElevenLabsModel   string

	// AudioClipsDir — diretório com os áudios pré-gravados (OGG/Opus), usado pelo
	// provider 'clips'. Vazio = banco de áudios desligado.
	AudioClipsDir string

	// AudioMatchMin — piso de semelhança entre a resposta escrita e a fala
	// gravada. 0.45 medido sobre o acervo real.
	AudioMatchMin float64
	// AudioMatchMaxMs — duração máxima da gravação que pode virar resposta.
	// A mediana do acervo é 6,5s; o teto existe contra os monólogos de 40s+.
	AudioMatchMaxMs int
	// AudioMatchShadow — decide, loga e mesmo assim responde em texto. Liga no
	// primeiro deploy para medir acerto sem risco; depois vira false.
	AudioMatchShadow bool

	// Agenda: funcionamento da escola e duração da aula experimental. Eram uma
	// frase fixa dentro do prompt — mudar o horário de sábado exigia recompilar.
	EscolaAbre     string
	EscolaFecha    string
	AulaDuracaoMin int
	// AgendaAutoConfirm — bot marca sozinho. Default false, de propósito.
	AgendaAutoConfirm bool

	// Google Agenda — mesmo cliente OAuth do ecossistema. Vazio = desligado.
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string
}

func LoadConfig() Config {
	return Config{
		Port:        getEnv("PORT", "3000"),
		BasePath:    getEnv("BASE_PATH", ""),
		DatabaseURL: mustEnv("DATABASE_URL"),
		RedisURL:    mustEnv("REDIS_URL"),
		TenantID:    mustEnv("TENANT_ID"),

		MetaAppSecret:          mustEnv("META_APP_SECRET"),
		MetaWebhookVerifyToken: mustEnv("META_WEBHOOK_VERIFY_TOKEN"),
		MetaPhoneNumberID:      getEnv("META_PHONE_NUMBER_ID", ""),
		MetaAccessToken:        getEnv("META_ACCESS_TOKEN", ""),

		AgentGoURL:    strings.TrimRight(getEnv("AGENT_GO_URL", "https://api.santos-tech.com"), "/"),
		BotModel:      getEnv("BOT_MODEL", "sonnet"),
		AgentGoSecret: mustEnv("AGENT_GO_SECRET"),

		SiteURL: strings.TrimRight(getEnv("SITE_URL", "https://santos-tech.com"), "/"),

		NotionToken: getEnv("NOTION_TOKEN", ""),
		// Data source "Agenda de Aulas" — a agenda que a escola realmente usa.
		//
		// O padrão anterior apontava para "Agenda — Aulas Experimentais", uma
		// base paralela com UMA linha de junho. O bot lia ela e marcava em cima
		// das 33 aulas reais, que estavam aqui o tempo todo.
		NotionExperimentalDSID: getEnv("NOTION_AGENDA_DS_ID", "fcbd4d0c-5173-462a-96a5-c8d05340ed14"),

		EvolutionWebhookSecret: getEnv("EVOLUTION_WEBHOOK_SECRET", ""),
		EvolutionAPIURL:        strings.TrimRight(getEnv("EVOLUTION_API_URL", ""), "/"),
		EvolutionAPIKey:        getEnv("EVOLUTION_API_KEY", ""),
		EvolutionInstance:      getEnv("EVOLUTION_INSTANCE", ""),

		AdminWhatsAppNumber: getEnv("ADMIN_WHATSAPP_NUMBER", ""),

		BGPoolSlots:       envInt("BG_POOL_SLOTS", 32),
		BGShutdownTimeout: time.Duration(envInt("BG_SHUTDOWN_TIMEOUT_SEC", 20)) * time.Second,

		OutboxBatchSize:      envInt("OUTBOX_BATCH_SIZE", 50),
		OutboxIdleIntervalMs: envInt("OUTBOX_IDLE_INTERVAL_MS", 500),
		OutboxMaxAttempts:    envInt("OUTBOX_MAX_ATTEMPTS", 5),
		OutboxClaimLease:     time.Duration(envInt("OUTBOX_CLAIM_LEASE_SEC", 300)) * time.Second,
		FollowUpConcurrency:  envInt("FOLLOW_UP_CONCURRENCY", 5),

		RetryStreamKey:      getEnv("RETRY_STREAM_KEY", "bot-go:webhook-retries"),
		RetryStreamGroup:    getEnv("RETRY_STREAM_GROUP", "bot-go-retry-workers"),
		RetryStreamConsumer: getEnv("RETRY_STREAM_CONSUMER", hostnameOr("bot-go-1")),
		// Safety net: com o stream cobrindo o caminho quente, o polling pode ser
		// bem mais espaçado que os 60s antigos. Cobre eventos órfãos (stream fora,
		// publish perdido) sem martelar o Postgres.
		RetryPollInterval: time.Duration(envInt("RETRY_POLL_INTERVAL_SEC", 300)) * time.Second,

		DebounceCacheTTL: time.Duration(envInt("DEBOUNCE_CACHE_TTL_SEC", 30)) * time.Second,

		FollowUpHasApprovedTemplates: getEnv("FOLLOW_UP_HAS_APPROVED_TEMPLATES", "false") == "true",
		FollowUpTemplateName:         getEnv("FOLLOW_UP_TEMPLATE_NAME", ""),
		FollowUpTemplateLanguage:     getEnv("FOLLOW_UP_TEMPLATE_LANGUAGE", "pt_BR"),

		Production: getEnv("NODE_ENV", "development") == "production",

		RateLimitEnabled: getEnv("RATE_LIMIT_ENABLED", "true") == "true",
		RatePhoneBurst:   envInt("RATE_PHONE_BURST", 10),
		RatePhonePerMin:  envInt("RATE_PHONE_PER_MIN", 10),
		RateGlobalBurst:  envInt("RATE_GLOBAL_BURST", 120),
		RateGlobalPerMin: envInt("RATE_GLOBAL_PER_MIN", 120),
		LLMDailyMax:      envInt("LLM_DAILY_MAX", 5000),

		DashAPIKey:     getEnv("DASH_API_KEY", ""),
		DashCORSOrigin: getEnv("DASH_CORS_ORIGIN", "https://santos-tech.com"),

		AuthMeURL:    getEnv("AUTH_ME_URL", "https://api.santos-tech.com/auth/me"),
		AuthTimeout:  time.Duration(envInt("AUTH_TIMEOUT_MS", 3000)) * time.Millisecond,
		AuthCacheTTL: time.Duration(envInt("AUTH_CACHE_TTL_SEC", 60)) * time.Second,

		CoolifyWebhookSecret: getEnv("COOLIFY_WEBHOOK_SECRET", ""),
		CoolifyAPIURL:        strings.TrimRight(getEnv("COOLIFY_API_URL", ""), "/"),
		CoolifyAPIToken:      getEnv("COOLIFY_API_TOKEN", ""),

		SentryOrgSlug:      getEnv("SENTRY_ORG_SLUG", ""),
		SentryAPIToken:     getEnv("SENTRY_API_TOKEN", ""),
		SentryPollInterval: time.Duration(envInt("SENTRY_POLL_INTERVAL_SEC", 300)) * time.Second,

		VoiceEnabled:   getEnv("VOICE_ENABLED", "false") == "true",
		OpenAIKey:      getEnv("OPENAI_API_KEY", ""),
		OpenAIBaseURL:  strings.TrimRight(getEnv("OPENAI_BASE_URL", "https://api.openai.com/v1"), "/"),
		OpenAITTSVoice: getEnv("OPENAI_TTS_VOICE", "nova"),
		OpenAITTSModel: getEnv("OPENAI_TTS_MODEL", "gpt-4o-mini-tts"),
		OpenAISTTModel: getEnv("OPENAI_STT_MODEL", "whisper-1"),

		ElevenLabsKey:     getEnv("ELEVENLABS_API_KEY", ""),
		ElevenLabsBaseURL: strings.TrimRight(getEnv("ELEVENLABS_BASE_URL", "https://api.elevenlabs.io/v1"), "/"),
		ElevenLabsModel:   getEnv("ELEVENLABS_MODEL", "eleven_multilingual_v2"),

		AudioClipsDir:    getEnv("AUDIO_CLIPS_DIR", ""),
		AudioMatchMin:    envFloat("AUDIO_MATCH_MIN", 0.45),
		AudioMatchMaxMs:  envInt("AUDIO_MATCH_MAX_MS", 12000),
		AudioMatchShadow: getEnv("AUDIO_MATCH_SHADOW", "true") == "true",

		EscolaAbre:        getEnv("ESCOLA_ABRE", "08:00"),
		EscolaFecha:       getEnv("ESCOLA_FECHA", "22:00"),
		AulaDuracaoMin:    envInt("AULA_DURACAO_MIN", 60),
		AgendaAutoConfirm: getEnv("AGENDA_AUTO_CONFIRM", "false") == "true",

		GoogleClientID:     getEnv("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret: getEnv("GOOGLE_CLIENT_SECRET", ""),
		GoogleRedirectURL:  getEnv("GOOGLE_CALENDAR_REDIRECT_URL", "https://api.santos-tech.com/bot/auth/google/callback"),
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

// hostnameOr devolve o hostname do processo (consumer name único por instância
// para o consumer group do stream) ou um fallback se indisponível.
func hostnameOr(fallback string) string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
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

// envFloat lê um float do ambiente; valor ilegível cai no default em vez de
// derrubar o boot — limiar mal digitado não pode tirar o bot do ar.
func envFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return n
}
