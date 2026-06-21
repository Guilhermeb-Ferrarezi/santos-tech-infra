package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleLogs busca logs no Loki com filtros estruturados e paginação por cursor.
// Admin-only. Responde 503 se LOKI_URL não estiver configurado.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.loki == nil || !s.loki.enabled() {
		writeErr(w, appErr(http.StatusServiceUnavailable, "LOGS_UNAVAILABLE", "Logs indisponível: LOKI_URL não configurado"))
		return
	}
	q := r.URL.Query()
	lq := lokiQueryFilters{
		Apps:        q["app"],
		Level:       strings.TrimSpace(q.Get("level")),
		StatusClass: strings.TrimSpace(q.Get("status_class")),
		StatusCode:  lokiAtoiOr(q.Get("status"), 0),
		Method:      strings.TrimSpace(q.Get("method")),
		Path:        strings.TrimSpace(q.Get("path")),
		RequestID:   strings.TrimSpace(q.Get("request_id")),
		User:        strings.TrimSpace(q.Get("user")),
		Search:      strings.TrimSpace(q.Get("q")),
		MinDurMs:    lokiAtoiOr(q.Get("min_dur"), 0),
		HTTPOnly:    q.Get("http_only") == "1",
	}
	dur, ok := lokiRangePresets[q.Get("range")]
	if !ok {
		dur = time.Hour
	}
	end := time.Now()
	appLabel := s.loki.getLokiAppLabel(r.Context())
	page, err := s.loki.lokiQuery(r.Context(), buildLokiQL(lq, appLabel), lokiPageParams{
		Start:  end.Add(-dur),
		End:    end,
		Limit:  lokiAtoiOr(q.Get("limit"), 50),
		Before: strings.TrimSpace(q.Get("before")),
		After:  strings.TrimSpace(q.Get("after")),
	})
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "LOKI_ERROR", "Erro ao consultar logs"))
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// handleLogLabels devolve os serviços disponíveis (label app/service_name) para
// popular o filtro de apps na tela de Logs.
func (s *Server) handleLogLabels(w http.ResponseWriter, r *http.Request) {
	if s.loki == nil || !s.loki.enabled() {
		writeErr(w, appErr(http.StatusServiceUnavailable, "LOGS_UNAVAILABLE", "Logs indisponível: LOKI_URL não configurado"))
		return
	}
	appLabel := s.loki.getLokiAppLabel(r.Context())
	apps, err := s.loki.lokiLabelValues(r.Context(), appLabel)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "LOKI_ERROR", "Erro ao consultar labels do Loki"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

func lokiAtoiOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}
