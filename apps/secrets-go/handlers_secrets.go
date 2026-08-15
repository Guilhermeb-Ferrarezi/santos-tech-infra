package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
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
		// ?all=true remove a lista inteira de uma vez (atômico — um Update só,
		// não N chamadas soltas do cliente). Sem ?all nem ?keyword, é no-op
		// (nenhuma keyword bate com string vazia) — mantido de propósito pra
		// não apagar tudo por engano num DELETE sem parâmetro nenhum.
		if r.URL.Query().Get("all") == "true" {
			st := s.state.Update(func(st *StateData) { st.Keywords = nil })
			s.hub.Broadcast(Event{Type: "status", Data: st})
			writeJSON(w, http.StatusOK, st.Keywords)
			return
		}
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

// tamanho de página da listagem de achados: default bem maior que o antigo
// (200) porque com o índice crescendo continuamente e o corte fixo antigo,
// achados relevantes (inclusive ATIVA) saíam da janela visível — ver
// ListHits (sort liveActive desc primeiro resolve a maior parte, mas o teto
// de 200 ainda limitava telas com muitos hits). Máximo de 2000 pra não
// pedir um payload gigante de uma vez só.
const (
	defaultReposSize = 1000
	maxReposSize     = 2000
)

func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	kw := r.URL.Query().Get("keyword")
	onlyCandidates := r.URL.Query().Get("all") != "true" // por padrão esconde os "candidato" (sem valor)
	size := defaultReposSize
	if raw := r.URL.Query().Get("size"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			size = n
		}
	}
	if size > maxReposSize {
		size = maxReposSize
	}
	hits, err := s.es.ListHits(r.Context(), kw, onlyCandidates, size)
	if err != nil {
		writeError(w, http.StatusBadGateway, "elasticsearch_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, hits)
}

// ── Presets de keywords ──────────────────────────────────────────────────
// Armazenamento à parte da lista "ativa" do scan — salvar/reaplicar
// conjuntos nomeados sem precisar redigitar ou colar de novo toda vez.

func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		presets, err := s.es.ListPresets(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, "elasticsearch_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, presets)

	case http.MethodPost:
		var body struct {
			Name     string   `json:"name"`
			Keywords []string `json:"keywords"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_body", "body inválido")
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "invalid_body", "nome do preset é obrigatório")
			return
		}
		keywords := dedupTrim(body.Keywords)
		if len(keywords) == 0 {
			writeError(w, http.StatusBadRequest, "invalid_body", "preset precisa de pelo menos 1 keyword")
			return
		}
		now := time.Now()
		p := KeywordPreset{ID: newPresetID(), Name: name, Keywords: keywords, CreatedAt: now, UpdatedAt: now}
		if err := s.es.PutPreset(r.Context(), p); err != nil {
			writeError(w, http.StatusBadGateway, "elasticsearch_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, p)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "método não permitido")
	}
}

func (s *Server) handlePresetByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "id do preset é obrigatório")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var body struct {
			Name     string   `json:"name"`
			Keywords []string `json:"keywords"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_body", "body inválido")
			return
		}
		name := strings.TrimSpace(body.Name)
		keywords := dedupTrim(body.Keywords)
		if name == "" || len(keywords) == 0 {
			writeError(w, http.StatusBadRequest, "invalid_body", "nome e ao menos 1 keyword são obrigatórios")
			return
		}
		p := KeywordPreset{ID: id, Name: name, Keywords: keywords, UpdatedAt: time.Now()}
		if err := s.es.PutPreset(r.Context(), p); err != nil {
			writeError(w, http.StatusBadGateway, "elasticsearch_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, p)

	case http.MethodDelete:
		if err := s.es.DeletePreset(r.Context(), id); err != nil {
			writeError(w, http.StatusBadGateway, "elasticsearch_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "método não permitido")
	}
}

func dedupTrim(raw []string) []string {
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		k := strings.TrimSpace(r)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	s.crawler.Stop()
	_ = s.es.ClearIndex(r.Context())
	_ = s.es.EnsureIndex(r.Context())
	s.hub.ResetKeywordCounts() // senão o ranking ficaria com números de hits que não existem mais
	st := s.state.Update(func(st *StateData) {
		st.Processed = nil
		st.TotalHits = 0
		st.LastError = ""
	})
	s.hub.Broadcast(Event{Type: "status", Data: st})
	s.hub.Broadcast(Event{Type: "stats", Data: map[string]int{}})
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}
