package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Revalidator refaz a verificação ativa de hits já indexados — sem re-fazer
// a busca no GitHub do zero. Cobre o caso "achei uma chave ativa e depois
// ela foi revogada" (ou o contrário: não deu pra confirmar na hora porque o
// provedor tava instável, mas a chave era real).
type Revalidator struct {
	es        *ElasticClient
	dotfy     *DotfyVerifier
	verifiers *GenericVerifier
	hub       *Hub
	state     *StateManager

	mu      sync.Mutex
	running bool
}

func NewRevalidator(es *ElasticClient, dotfy *DotfyVerifier, verifiers *GenericVerifier, hub *Hub, state *StateManager) *Revalidator {
	return &Revalidator{es: es, dotfy: dotfy, verifiers: verifiers, hub: hub, state: state}
}

func (r *Revalidator) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// Run revalida todos os hits com valor capturado e verificador disponível.
// Só uma revalidação por vez — uma chamada concorrente enquanto já tem uma
// rodando é ignorada (retorna sem fazer nada).
func (r *Revalidator) Run(ctx context.Context) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	hits, err := r.es.ListVerifiableHits(ctx)
	if err != nil {
		r.hub.Broadcast(Event{Type: "log", Data: "revalidação: erro buscando hits: " + err.Error()})
		return
	}

	toCheck := make([]Hit, 0, len(hits))
	for _, h := range hits {
		if h.VerifierFamily == "" {
			// hit indexado antes desse campo existir (ou nunca teve family
			// resolvida) — deriva de novo a partir do que já foi salvo, sem
			// precisar buscar o conteúdo no GitHub outra vez.
			_, valueFamily := fingerprintProviderFamily(h.MatchedValue)
			_, kwFamily := keywordFamily(h.Keyword)
			h.VerifierFamily = kwFamily
			if h.VerifierFamily == "" {
				h.VerifierFamily = valueFamily
			}
		}
		if h.VerifierFamily != "" {
			toCheck = append(toCheck, h)
		}
	}
	if len(toCheck) == 0 {
		r.hub.Broadcast(Event{Type: "log", Data: "revalidação: nenhuma chave verificável no índice"})
		return
	}

	r.hub.Broadcast(Event{Type: "log", Data: fmt.Sprintf("revalidação: iniciando, %d chave(s) pra reconferir", len(toCheck))})

	// paralelo, mas com teto: cada verificação chama um provedor diferente
	// (sem rate limiter compartilhado como a Search API do GitHub tem), mas
	// não custa nada segurar um limite responsável de chamadas simultâneas
	// contra APIs de terceiros.
	const concurrency = 25
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	checked, active, done := 0, 0, 0

	for _, h := range toCheck {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		default:
		}

		h := h
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			var liveChecked, liveActive, liveSandbox bool
			if h.VerifierFamily == "dotfy" {
				liveChecked, liveActive = r.dotfy.CheckKey(ctx, h.MatchedValue)
			} else {
				liveChecked, liveActive, liveSandbox = r.verifiers.CheckKey(ctx, h.VerifierFamily, h.MatchedValue)
			}

			if liveChecked {
				now := time.Now()
				h.LiveChecked = liveChecked
				h.LiveActive = liveActive
				h.LiveSandbox = liveSandbox
				h.LastVerifiedAt = &now
				if err := r.es.IndexHit(ctx, h); err != nil {
					log.Println("revalidação: erro reindexando:", err)
				} else {
					r.hub.Broadcast(Event{Type: "hit", Data: h})
				}
			}

			mu.Lock()
			done++
			if liveChecked {
				checked++
				if liveActive {
					active++
				}
			}
			d, tot, act := done, len(toCheck), active
			mu.Unlock()

			if d%20 == 0 || d == tot {
				r.hub.Broadcast(Event{Type: "log", Data: fmt.Sprintf(
					"revalidação: %d/%d checadas (%d confirmadas ativas até agora)", d, tot, act,
				)})
			}
		}()
	}
	wg.Wait()

	now := time.Now()
	s := r.state.Update(func(s *StateData) { s.LastRevalidationAt = &now })
	r.hub.Broadcast(Event{Type: "status", Data: s})
	r.hub.Broadcast(Event{Type: "log", Data: fmt.Sprintf(
		"revalidação concluída: %d checadas, %d confirmadas ativas", checked, active,
	)})
}
