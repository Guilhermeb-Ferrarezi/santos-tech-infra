package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// scanOptions são as decisões de scan tiradas do state no momento do Start.
// Passadas por valor pra cada keyword — mudanças de config durante um scan
// só valem no próximo.
type scanOptions struct {
	skipExamples    bool
	pageConcurrency int
	maxPages        int
	includeForks    bool
	dateSplit       bool
	dateSplitYears  int
}

// dateSplitQueries fatia a busca de uma keyword em janelas de data
// (qualifier created:) de 2015 até o fim do ano atual. A Search API do
// GitHub tem teto duro de 1000 resultados por query — pra keyword popular
// (OPENAI_API_KEY, AKIA...) isso deixa a maior parte dos repos invisível.
// Cada janela é uma busca independente, então N janelas = até N×1000
// resultados pra mesma keyword.
func dateSplitQueries(keyword string, years int) []string {
	const startYear = 2015 // GitHub já indexava código de sobra desde então
	now := time.Now()
	var queries []string
	for from := startYear; from <= now.Year(); from += years {
		to := from + years - 1
		if to > now.Year() {
			to = now.Year()
		}
		queries = append(queries, fmt.Sprintf("%s created:%d-01-01..%d-12-31", keyword, from, to))
	}
	return queries
}

// Crawler orquestra o scan com um pool de workers: várias keywords em
// paralelo (cada worker pega uma da fila), cada uma verificando vários
// arquivos em paralelo por página de busca. Todos os workers dividem o
// mesmo pool de tokens do GitHubClient (searchAvail/contentAvail) — com um
// só token, paralelizar keywords não estoura o limite da Search API, só usa
// melhor o tempo ocioso entre chamadas; com vários tokens (GITHUB_TOKENS),
// o pool também aumenta o throughput real disponível pros workers. Start/Stop
// controlam um context.CancelFunc — é o "liga/desliga" que você pediu.
type Crawler struct {
	gh        *GitHubClient
	es        *ElasticClient
	state     *StateManager
	hub       *Hub
	dotfy     *DotfyVerifier
	verifiers *GenericVerifier

	mu     sync.Mutex
	cancel context.CancelFunc
}

func NewCrawler(gh *GitHubClient, es *ElasticClient, state *StateManager, hub *Hub, dotfy *DotfyVerifier, verifiers *GenericVerifier) *Crawler {
	return &Crawler{gh: gh, es: es, state: state, hub: hub, dotfy: dotfy, verifiers: verifiers}
}

func (c *Crawler) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancel != nil
}

func (c *Crawler) Start() bool {
	c.mu.Lock()
	if c.cancel != nil {
		c.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.mu.Unlock()

	now := time.Now()
	s := c.state.Update(func(s *StateData) {
		s.Running = true
		s.Processed = nil
		s.CurrentKeywords = nil
		s.LastError = ""
		s.StartedAt = &now
		s.StoppedAt = nil
	})
	c.hub.Broadcast(Event{Type: "status", Data: s})

	go c.run(ctx)
	return true
}

func (c *Crawler) Stop() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *Crawler) run(ctx context.Context) {
	defer func() {
		c.mu.Lock()
		c.cancel = nil
		c.mu.Unlock()

		// contexto novo (não o do scan, que pode já estar cancelado) pra
		// conseguir buscar a contagem final mesmo depois de um stop.
		finalTotal, _ := c.es.CountHits(context.Background())

		now := time.Now()
		s := c.state.Update(func(s *StateData) {
			s.Running = false
			s.CurrentKeywords = nil
			s.StoppedAt = &now
			s.TotalHits = finalTotal
		})
		c.hub.Broadcast(Event{Type: "status", Data: s})
		c.hub.Broadcast(Event{Type: "log", Data: "scan finalizado/interrompido"})
	}()

	if err := c.es.EnsureIndex(ctx); err != nil {
		c.state.Update(func(s *StateData) { s.LastError = err.Error() })
		c.hub.Broadcast(Event{Type: "log", Data: "erro criando índice ES: " + err.Error()})
		return
	}

	settings := c.state.Get()
	numWorkers := clampKeywordWorkers(settings.KeywordWorkers)
	opts := scanOptions{
		skipExamples:    settings.SkipExamples,
		pageConcurrency: clampPageConcurrency(settings.PageConcurrency),
		maxPages:        clampMaxPages(settings.MaxPages),
		includeForks:    settings.IncludeForks,
		dateSplit:       settings.DateSplit,
		dateSplitYears:  clampDateSplitYears(settings.DateSplitYears),
	}

	kwChan := make(chan string)
	go func() {
		defer close(kwChan)
		for _, kw := range settings.Keywords {
			select {
			case <-ctx.Done():
				return
			case kwChan <- kw:
			}
		}
	}()

	c.hub.Broadcast(Event{Type: "log", Data: fmt.Sprintf(
		"iniciando: %d worker(s) de keyword, %d verificações em paralelo por página, máx %d página(s)/keyword, forks: %v, split por data (%d ano(s)): %v",
		numWorkers, opts.pageConcurrency, opts.maxPages, opts.includeForks, opts.dateSplitYears, opts.dateSplit,
	)})

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for kw := range kwChan {
				if ctx.Err() != nil {
					return
				}
				c.processKeyword(ctx, kw, opts)
			}
		}()
	}
	wg.Wait()
}

// processKeyword faz a busca completa (todas as páginas e janelas de data)
// de uma keyword e indexa os hits. Chamado por qualquer worker do pool —
// várias keywords diferentes podem estar aqui dentro ao mesmo tempo.
func (c *Crawler) processKeyword(ctx context.Context, kw string, opts scanOptions) {
	s := c.state.Update(func(s *StateData) { s.CurrentKeywords = append(s.CurrentKeywords, kw) })
	c.hub.Broadcast(Event{Type: "status", Data: s})

	queries := []string{kw}
	if opts.dateSplit {
		queries = dateSplitQueries(kw, opts.dateSplitYears)
	}

	for _, q := range queries {
		if ctx.Err() != nil {
			break
		}
		fullQ := q
		if opts.includeForks {
			fullQ += " fork:true"
		}
		c.hub.Broadcast(Event{Type: "log", Data: "buscando: " + fullQ})

		page := 0
		err := c.gh.SearchCode(ctx, fullQ, opts.maxPages, func(items []CodeSearchItem, total int) error {
			// sem isso, uma keyword genérica com muitos resultados (ex: "AKIA")
			// fica minutos sem dar sinal de vida — o rate limiter da Search API
			// é compartilhado entre todos os workers, então com vários rodando
			// em paralelo cada um avança bem mais devagar do que parece.
			page++
			c.hub.Broadcast(Event{Type: "log", Data: fmt.Sprintf(
				"%s: página %d — %d/%d resultados", q, page, min(page*100, total), total,
			)})

			var wg sync.WaitGroup
			sem := make(chan struct{}, opts.pageConcurrency)

			for _, item := range items {
				item := item
				if opts.skipExamples && isExamplePath(item.Path) {
					continue
				}
				wg.Add(1)
				sem <- struct{}{}
				go func() {
					defer wg.Done()
					defer func() { <-sem }()
					c.processItem(ctx, kw, item)
				}()
			}
			wg.Wait()
			return nil
		})

		if err != nil {
			c.state.Update(func(s *StateData) { s.LastError = err.Error() })
			c.hub.Broadcast(Event{Type: "log", Data: "erro em '" + q + "': " + err.Error()})
			break
		}
	}

	// removeu do "buscando agora" independente de sucesso/erro/cancelamento
	s = c.state.Update(func(s *StateData) {
		out := s.CurrentKeywords[:0]
		for _, k := range s.CurrentKeywords {
			if k != kw {
				out = append(out, k)
			}
		}
		s.CurrentKeywords = out
	})

	if ctx.Err() != nil {
		// fomos cancelados (Stop) no meio desta keyword — não marca como
		// processada, o defer do run() cuida do total final.
		c.hub.Broadcast(Event{Type: "status", Data: s})
		return
	}

	total, _ := c.es.CountHits(ctx)
	s = c.state.Update(func(s *StateData) {
		s.Processed = append(s.Processed, kw)
		s.TotalHits = total
	})
	c.hub.Broadcast(Event{Type: "status", Data: s})
}

// processItem verifica um único arquivo achado pela Search API: baixa o
// conteúdo real, confere se tem valor de verdade, identifica o provedor e
// roda a verificação ativa quando disponível.
func (c *Crawler) processItem(ctx context.Context, kw string, item CodeSearchItem) {
	// dá visibilidade de QUAL repo/arquivo está sendo checado agora, não só
	// quando acha algo — a maioria dos itens não vira hit, então sem isso o
	// feed fica quieto demais.
	c.hub.Broadcast(Event{Type: "scanning", Data: map[string]string{
		"repo": item.Repository.FullName,
		"path": item.Path,
	}})

	hit := Hit{
		Keyword:      kw,
		RepoFullName: item.Repository.FullName,
		RepoURL:      item.Repository.HTMLURL,
		FilePath:     item.Path,
		FileURL:      item.HTMLURL,
		Private:      item.Repository.Private,
		OwnRepo:      isOwnRepo(item.Repository.FullName),
		IndexedAt:    time.Now(),
	}

	// a Search API é um pré-filtro barulhento (tokeniza, dá falso positivo,
	// e cita nome de variável sem valor) — busca o conteúdo real e confere
	// se tem um VALOR de verdade.
	var liveCandidate, family string
	if content, err := c.gh.FetchFileContent(ctx, item.ContentURL); err == nil {
		hit.HasCandidate, hit.MatchedValue, liveCandidate = verifyContent(kw, content)
		if hit.HasCandidate {
			// o rótulo da UI vem do VALOR (é o que detecta mismatch tipo
			// "achei chave da OpenAI dentro de ABACATEPAY_API_KEY"). Mas
			// alguns provedores (Mercado Pago, Asaas, Pagar.me...) não têm
			// prefixo público reconhecível no valor — nesses casos o nome
			// da keyword decide qual verificador usar, e também preenche o
			// rótulo se o valor não tiver dado nenhuma pista sozinho.
			valueProvider, valueFamily := fingerprintProviderFamily(hit.MatchedValue)
			kwProvider, kwFamily := keywordFamily(kw)

			hit.GuessedProvider = valueProvider
			if hit.GuessedProvider == "" {
				hit.GuessedProvider = kwProvider
			}

			family = kwFamily
			if family == "" {
				family = valueFamily
			}
		}
	}

	hit.VerifierFamily = family

	// verificação ativa: cada provedor reconhecido tem um endpoint oficial
	// de healthcheck/identidade (read-only). OwnRepo fica só como metadado
	// informativo na UI.
	switch family {
	case "":
		// nenhum verificador disponível pra esse padrão
	case "dotfy":
		if liveCandidate != "" {
			now := time.Now()
			hit.LiveChecked, hit.LiveActive = c.dotfy.CheckKey(ctx, liveCandidate)
			hit.LastVerifiedAt = &now
		}
	default:
		now := time.Now()
		hit.LiveChecked, hit.LiveActive, hit.LiveSandbox = c.verifiers.CheckKey(ctx, family, hit.MatchedValue)
		hit.LastVerifiedAt = &now
	}

	if err := c.es.IndexHit(ctx, hit); err != nil {
		log.Println("erro indexando hit:", err)
		return
	}
	c.hub.Broadcast(Event{Type: "hit", Data: hit})
	// Empurra o ranking atualizado no mesmo canal SSE — o front escreve direto
	// no cache, sem precisar (re)buscar via REST a cada hit (ver hub.go).
	c.hub.Broadcast(Event{Type: "stats", Data: c.hub.IncKeywordCount(hit.Keyword)})
}
