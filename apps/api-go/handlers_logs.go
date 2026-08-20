package main

import (
	"net/http"
	"sort"
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
		Limit:  clampLimit(q.Get("limit"), 50, lokiMaxPageLimit),
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

// handleTopIPs devolve os N IPs com mais requisições no intervalo.
// Busca entradas de log com campo "ip" no Loki e agrega em Go.
// Parâmetros: range (15m/1h/6h/24h/7d, default 1h), limit (default 20, max 50).
func (s *Server) handleTopIPs(w http.ResponseWriter, r *http.Request) {
	if s.loki == nil || !s.loki.enabled() {
		writeErr(w, appErr(http.StatusServiceUnavailable, "LOGS_UNAVAILABLE", "Logs indisponível: LOKI_URL não configurado"))
		return
	}
	q := r.URL.Query()
	dur, ok := lokiRangePresets[q.Get("range")]
	if !ok {
		dur = time.Hour
	}
	limit := lokiAtoiOr(q.Get("limit"), 20)
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	appLabel := s.loki.getLokiAppLabel(r.Context())
	// Busca entradas HTTP (que têm campo "ip") e agrega em Go.
	// Limite de 500 entradas cobre picos normais; ajustar LOG_BODY_MAX_BYTES
	// no serviço se precisar de janelas maiores.
	logql := `{` + appLabel + `=~".+"} | json | ip != ""`
	end := time.Now()
	page, err := s.loki.lokiQuery(r.Context(), logql, lokiPageParams{
		Start: end.Add(-dur),
		End:   end,
		Limit: 500,
	})
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "LOKI_ERROR", "Erro ao consultar Loki"))
		return
	}
	counts := make(map[string]int64, 64)
	for _, e := range page.Entries {
		if e.Fields == nil {
			continue
		}
		if ip, ok := e.Fields["ip"].(string); ok && ip != "" {
			counts[ip]++
		}
	}
	out := make([]topIPEntry, 0, len(counts))
	for ip, c := range counts {
		out = append(out, topIPEntry{IP: ip, Count: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"ips": out})
}

// lokiMaxPageLimit é o teto de entradas por página que aceitamos do cliente
// (mesmo teto que o lokiClient aplica internamente).
const lokiMaxPageLimit = 500

// clampLimit lê o ?limit= do cliente e o encaixa em [1, max], caindo no default
// quando ausente ou inválido. Os clientes de Loki/Sentry já recusam valores fora
// da faixa, mas caindo no DEFAULT — ou seja, um ?limit=1000000 devolvia 50 itens
// em silêncio, em vez do máximo pedido. Clampar aqui deixa o teto explícito e o
// comportamento previsível, e garante o limite mesmo se o cliente mudar.
func clampLimit(raw string, def, max int) int {
	n := lokiAtoiOr(raw, def)
	switch {
	case n <= 0:
		return def
	case n > max:
		return max
	}
	return n
}

func lokiAtoiOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}
