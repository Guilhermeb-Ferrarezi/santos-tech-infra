package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// Handlers do domínio (scan de secrets vazados) — portados de
// dotfy-scanner/backend/handlers.go, adaptados de métodos de *App para
// métodos de *Server (mesmo padrão dos outros serviços Go do ecossistema) e
// com writeJSON/writeError incluindo status code + envelope {code,message}
// nos erros, em vez de http.Error em texto puro. Lógica de negócio idêntica.

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		StateData
		Revalidating bool `json:"revalidating"`
	}{StateData: s.state.Get(), Revalidating: s.revalidator.IsRunning()})
}

func (s *Server) handleRevalidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "método não permitido")
		return
	}
	if s.revalidator.IsRunning() {
		writeJSON(w, http.StatusOK, map[string]any{"started": false, "reason": "já tem uma revalidação rodando"})
		return
	}
	go s.revalidator.Run(context.Background())
	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	started := s.crawler.Start()
	writeJSON(w, http.StatusOK, map[string]any{"started": started})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.crawler.Stop()
	writeJSON(w, http.StatusOK, map[string]any{"stopping": true})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "método não permitido")
		return
	}
	var body struct {
		SkipExamples          *bool `json:"skipExamples"`
		KeywordWorkers        *int  `json:"keywordWorkers"`
		PageConcurrency       *int  `json:"pageConcurrency"`
		MaxPages              *int  `json:"maxPages"`
		AutoRevalidate        *bool `json:"autoRevalidate"`
		AutoRevalidateMaxRuns *int  `json:"autoRevalidateMaxRuns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "body inválido")
		return
	}
	st := s.state.Update(func(st *StateData) {
		if body.SkipExamples != nil {
			st.SkipExamples = *body.SkipExamples
		}
		if body.KeywordWorkers != nil {
			st.KeywordWorkers = clampKeywordWorkers(*body.KeywordWorkers)
		}
		if body.PageConcurrency != nil {
			st.PageConcurrency = clampPageConcurrency(*body.PageConcurrency)
		}
		if body.MaxPages != nil {
			st.MaxPages = clampMaxPages(*body.MaxPages)
		}
		if body.AutoRevalidate != nil {
			if *body.AutoRevalidate && !st.AutoRevalidate {
				st.AutoRevalidateRunCount = 0 // religou — reseta a contagem
			}
			st.AutoRevalidate = *body.AutoRevalidate
		}
		if body.AutoRevalidateMaxRuns != nil {
			n := *body.AutoRevalidateMaxRuns
			if n < 0 {
				n = 0
			}
			st.AutoRevalidateMaxRuns = n
		}
	})
	s.hub.Broadcast(Event{Type: "status", Data: st})
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleKeywords(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.state.Get().Keywords)

	case http.MethodPost:
		var body struct {
			Keyword  string   `json:"keyword"`
			Keywords []string `json:"keywords"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_body", `body inválido, esperado {"keyword": "..."} ou {"keywords": [...]}`)
			return
		}
		toAdd := body.Keywords
		if body.Keyword != "" {
			toAdd = append(toAdd, body.Keyword)
		}
		if len(toAdd) == 0 {
			writeError(w, http.StatusBadRequest, "invalid_body", "nenhuma keyword informada")
			return
		}
		st := s.state.Update(func(st *StateData) {
			existing := make(map[string]bool, len(st.Keywords))
			for _, k := range st.Keywords {
				existing[k] = true
			}
			for _, raw := range toAdd {
				k := strings.TrimSpace(raw)
				if k == "" || existing[k] {
					continue
				}
				existing[k] = true
				st.Keywords = append(st.Keywords, k)
			}
		})
		s.hub.Broadcast(Event{Type: "status", Data: st})
		writeJSON(w, http.StatusOK, st.Keywords)

	case http.MethodDelete:
		kw := r.URL.Query().Get("keyword")
		st := s.state.Update(func(st *StateData) {
			out := st.Keywords[:0]
			for _, k := range st.Keywords {
				if k != kw {
					out = append(out, k)
				}
			}
			st.Keywords = out
		})
		s.hub.Broadcast(Event{Type: "status", Data: st})
		writeJSON(w, http.StatusOK, st.Keywords)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "método não permitido")
	}
}

func (s *Server) handleKeywordStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.es.KeywordStats(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "elasticsearch_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	kw := r.URL.Query().Get("keyword")
	onlyCandidates := r.URL.Query().Get("all") != "true" // por padrão esconde os "candidato" (sem valor)
	hits, err := s.es.ListHits(r.Context(), kw, onlyCandidates, 200)
	if err != nil {
		writeError(w, http.StatusBadGateway, "elasticsearch_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, hits)
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	s.crawler.Stop()
	_ = s.es.ClearIndex(r.Context())
	_ = s.es.EnsureIndex(r.Context())
	st := s.state.Update(func(st *StateData) {
		st.Processed = nil
		st.TotalHits = 0
		st.LastError = ""
	})
	s.hub.Broadcast(Event{Type: "status", Data: st})
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}
