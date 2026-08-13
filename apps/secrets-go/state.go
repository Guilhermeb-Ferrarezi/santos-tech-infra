package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// stateFile é configurável via STATE_FILE (ver config.go) — em produção
// (Coolify) aponta pra um volume montado em /data (senão o progresso se
// perde a cada deploy/restart do container).
var stateFile = envOr("STATE_FILE", "/data/state.json")

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type StateData struct {
	Running            bool       `json:"running"`
	Keywords           []string   `json:"keywords"`
	Processed          []string   `json:"processed"`
	CurrentKeywords    []string   `json:"currentKeywords"` // uma por worker ativo agora
	TotalHits          int        `json:"totalHits"`
	SkipExamples       bool       `json:"skipExamples"`    // pula arquivos tipo .env.example (placeholder por definição)
	KeywordWorkers     int        `json:"keywordWorkers"`  // quantas keywords processadas em paralelo
	PageConcurrency    int        `json:"pageConcurrency"` // quantos arquivos verificados em paralelo por página de busca
	MaxPages           int        `json:"maxPages"`        // teto de páginas de busca por keyword (100 resultados/página)
	StartedAt          *time.Time `json:"startedAt,omitempty"`
	StoppedAt          *time.Time `json:"stoppedAt,omitempty"`
	LastRevalidationAt *time.Time `json:"lastRevalidationAt,omitempty"` // última vez que a verificação ativa de hits já achados rodou de novo
	LastError          string     `json:"lastError,omitempty"`

	// controle do loop de revalidação automática em background (roda a cada
	// revalidateInterval — ver main.go). Sem isso ele rodaria pra sempre.
	AutoRevalidate         bool `json:"autoRevalidate"`         // liga/desliga o loop
	AutoRevalidateMaxRuns  int  `json:"autoRevalidateMaxRuns"`  // 0 = sem limite; N = para sozinho depois de N execuções
	AutoRevalidateRunCount int  `json:"autoRevalidateRunCount"` // quantas vezes já rodou (reseta se você desligar e ligar de novo)
}

// Limites responsáveis: os verificadores ativos (Stripe, OpenAI, GitHub...)
// não têm rate limiter próprio como o GitHubClient tem pra Search API —
// KeywordWorkers × PageConcurrency é o teto de chamadas simultâneas que
// podemos disparar contra APIs de terceiros de uma vez. Não deixa crescer
// sem limite. Tetos dobrados junto com o pool de tokens do GitHub
// (GITHUB_TOKENS) — mais tokens = mais throughput real de busca disponível
// pros workers consumirem, mas o teto continua existindo pra não hostilizar
// as APIs de verificação de terceiros.
const (
	minKeywordWorkers     = 1
	maxKeywordWorkers     = 16
	defaultKeywordWorkers = 3

	minPageConcurrency     = 1
	maxPageConcurrency     = 40
	defaultPageConcurrency = 5

	// GitHub permite até 10 páginas (1000 resultados) por busca, mas pra
	// keyword genérica (tipo "AKIA") isso é 1000 resultados majoritariamente
	// irrelevantes — o sinal real está quase sempre nas primeiras páginas.
	// Default mais baixo corta bastante o tempo de scan sem perder muita coisa.
	minMaxPages     = 1
	maxMaxPages     = 10
	defaultMaxPages = 3
)

func clampKeywordWorkers(n int) int {
	if n < minKeywordWorkers {
		return defaultKeywordWorkers
	}
	if n > maxKeywordWorkers {
		return maxKeywordWorkers
	}
	return n
}

func clampPageConcurrency(n int) int {
	if n < minPageConcurrency {
		return defaultPageConcurrency
	}
	if n > maxPageConcurrency {
		return maxPageConcurrency
	}
	return n
}

func clampMaxPages(n int) int {
	if n < minMaxPages {
		return defaultMaxPages
	}
	if n > maxMaxPages {
		return maxMaxPages
	}
	return n
}

// StateManager protege o estado com um mutex e persiste em disco a cada
// mudança, então dá pra "desligar" o processo e religar sem perder o progresso.
type StateManager struct {
	mu   sync.RWMutex
	data StateData
}

func NewStateManager() *StateManager {
	sm := &StateManager{data: StateData{
		Keywords:        defaultKeywords(),
		SkipExamples:    true,
		KeywordWorkers:  defaultKeywordWorkers,
		PageConcurrency: defaultPageConcurrency,
		MaxPages:        defaultMaxPages,
		AutoRevalidate:  true,
	}}
	if b, err := os.ReadFile(stateFile); err == nil {
		_ = json.Unmarshal(b, &sm.data)
	}
	sm.data.Running = false       // nunca reabre como "rodando" após restart do processo
	sm.data.CurrentKeywords = nil // idem — não tinha nada rodando de verdade
	if len(sm.data.Keywords) == 0 {
		// state.json corrompido/vazio não pode deixar a lista de keywords
		// sumir silenciosamente — volta pros defaults.
		sm.data.Keywords = defaultKeywords()
	}
	sm.data.KeywordWorkers = clampKeywordWorkers(sm.data.KeywordWorkers)
	sm.data.PageConcurrency = clampPageConcurrency(sm.data.PageConcurrency)
	sm.data.MaxPages = clampMaxPages(sm.data.MaxPages)
	return sm
}

func (sm *StateManager) Get() StateData {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return cloneState(sm.data)
}

func (sm *StateManager) Update(fn func(*StateData)) StateData {
	sm.mu.Lock()
	fn(&sm.data)
	cp := cloneState(sm.data)
	sm.mu.Unlock()
	persistState(cp)
	return cp
}

func cloneState(d StateData) StateData {
	cp := d
	cp.Keywords = append([]string{}, d.Keywords...)
	cp.Processed = append([]string{}, d.Processed...)
	cp.CurrentKeywords = append([]string{}, d.CurrentKeywords...)
	return cp
}

func persistState(d StateData) {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(stateFile, b, 0644)
}

// defaultKeywords é a lista completa (pagamentos BR/globais, IA, cloud,
// comunicação, auth/analytics, dev tooling) — o mesmo conjunto construído
// manualmente ao longo do uso local do dotfy-scanner, só que versionado
// aqui como default pra não depender de reconstruir na mão a cada
// state.json novo/vazio (ver NewStateManager: reseta pra isso se o volume
// vier vazio ou corrompido). "// verificado" marca as que já têm
// verificação ativa em verifiers.go — o resto fica só no fingerprint
// passivo (sem checar contra a API do provedor).
func defaultKeywords() []string {
	return []string{
		"vk_test_",
		"vk_live_",
		"vk_prod_",
		"DOTFY_API_KEY",
		"DOTFY_SECRET_KEY",
		"dotfy_key",
		"dotfy_secret",
		"dotfy_token",
		"x-dotfy-api-key",
		"DOTFY_WEBHOOK_SECRET",

		// Pagamentos — Brasil
		"APP_USR",
		"sb_secret",
		"ABACATEPAY_API_KEY",       // verificado
		"PAGARME_API_KEY",          // verificado
		"MERCADOPAGO_ACCESS_TOKEN", // verificado
		"ASAAS_API_KEY",            // verificado
		"PAGSEGURO_TOKEN",
		"EFI_CLIENT_SECRET",
		"IUGU_API_TOKEN",
		"JUNO_API_TOKEN",
		"VINDI_API_KEY",
		"STARK_BANK_PRIVATE_KEY",
		"COBRE_API_KEY",
		"CELCOIN_CLIENT_SECRET",
		"OPENPIX_APP_ID",
		"ZOOP_MARKETPLACE_KEY",
		"CIELO_MERCHANT_KEY",
		"REDE_TOKEN",
		"GETNET_CLIENT_SECRET",

		// Pagamentos — Global
		"STRIPE_API_KEY", // verificado
		"PAYPAL_CLIENT_SECRET",
		"SQUARE_ACCESS_TOKEN", // verificado
		"BRAINTREE_PRIVATE_KEY",
		"ADYEN_API_KEY",
		"RAZORPAY_KEY_SECRET",
		"PLAID_SECRET",

		// IA / LLM
		"OPENAI_API_KEY",        // verificado
		"ANTHROPIC_API_KEY",     // verificado
		"CLAUDE_API_KEY",        // verificado (mesmo verificador do Anthropic)
		"GROQ_API_KEY",          // verificado
		"MISTRAL_API_KEY",       // verificado
		"COHERE_API_KEY",        // verificado
		"REPLICATE_API_TOKEN",   // verificado
		"HUGGINGFACE_API_TOKEN", // verificado
		"ELEVENLABS_API_KEY",    // verificado
		"GEMINI_API_KEY",        // verificado
		"OPENROUTER_API_KEY",    // verificado
		"TOGETHER_API_KEY",      // verificado
		"FIREWORKS_API_KEY",     // verificado
		"XAI_API_KEY",           // verificado
		"GROK_API_KEY",          // verificado (mesmo verificador do xAI)
		"DEEPSEEK_API_KEY",      // verificado
		"STABILITY_API_KEY",     // verificado
		"DEEPGRAM_API_KEY",      // verificado
		"ASSEMBLYAI_API_KEY",    // verificado
		"PERPLEXITY_API_KEY",
		"AZURE_OPENAI_KEY",
		"AI21_API_KEY",
		"VOYAGE_API_KEY",
		"RUNWAY_API_KEY",
		"LANGSMITH_API_KEY",

		// Cloud / Infra
		"CLOUDFLARE_API_TOKEN",      // verificado
		"DIGITALOCEAN_ACCESS_TOKEN", // verificado
		"VERCEL_TOKEN",              // verificado
		"NETLIFY_AUTH_TOKEN",        // verificado
		"HEROKU_API_KEY",            // verificado
		"NPM_TOKEN",                 // verificado
		"AWS_SECRET_ACCESS_KEY",
		"AWS_ACCESS_KEY_ID",
		"SUPABASE_SERVICE_ROLE_KEY",
		"RAILWAY_TOKEN", // verificado
		"FLY_API_TOKEN",
		"GITLAB_TOKEN",    // verificado
		"CIRCLECI_TOKEN",  // verificado
		"DATADOG_API_KEY", // verificado

		// Comunicação
		"DISCORD_BOT_TOKEN", // verificado
		"MAILGUN_API_KEY",   // verificado
		"SENDGRID_API_KEY",  // verificado
		"SLACK_BOT_TOKEN",   // verificado
		"TWILIO_AUTH_TOKEN",
		"TELEGRAM_BOT_TOKEN", // verificado
		"RESEND_API_KEY",     // verificado

		// Auth / Analytics / Observabilidade
		"GITHUB_TOKEN", // verificado
		"AUTH0_CLIENT_SECRET",
		"CLERK_SECRET_KEY", // verificado
		"FIREBASE_API_KEY", // verificado
		"MIXPANEL_TOKEN",
		"SEGMENT_WRITE_KEY",
		"POSTHOG_API_KEY",
		"SENTRY_AUTH_TOKEN", // verificado

		// Produtividade / dev tooling
		"NOTION_API_KEY", // verificado
		"NOTION_TOKEN",   // verificado
		"FIGMA_TOKEN",    // verificado
		"LINEAR_API_KEY",
		"SHOPIFY_ACCESS_TOKEN",
		"ALGOLIA_API_KEY",
		"GOOGLE_MAPS_API_KEY",
		"DOCKER_HUB_TOKEN",
	}
}
