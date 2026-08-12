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
// sem limite.
const (
	minKeywordWorkers     = 1
	maxKeywordWorkers     = 8
	defaultKeywordWorkers = 3

	minPageConcurrency     = 1
	maxPageConcurrency     = 20
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
	}
}
