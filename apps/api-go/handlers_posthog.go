package main

import (
	"context"
	"fmt"
	"net/http"
)

// posthogRangeDays valida o parâmetro "range" contra uma whitelist fixa e
// devolve o número de dias — nunca interpola o valor bruto da query string
// na HogQL, pra não abrir brecha de injeção.
func posthogRangeDays(v string) (int, error) {
	switch v {
	case "", "7d":
		return 7, nil
	case "30d":
		return 30, nil
	case "90d":
		return 90, nil
	default:
		return 0, appErr(http.StatusBadRequest, "BAD_REQUEST", "range inválido (use 7d, 30d ou 90d)")
	}
}

// posthogUnavailable escreve 503 se a PostHog não estiver configurada
// (POSTHOG_PERSONAL_API_KEY/POSTHOG_PROJECT_ID vazios) e devolve true nesse caso.
func (s *Server) posthogUnavailable(w http.ResponseWriter) bool {
	if s.posthog == nil || !s.posthog.enabled() {
		writeErr(w, appErr(http.StatusServiceUnavailable, "POSTHOG_UNAVAILABLE", "Analytics indisponível: POSTHOG_PERSONAL_API_KEY não configurado"))
		return true
	}
	return false
}

type postHogTotals struct {
	Pageviews int `json:"pageviews"`
	Visitors  int `json:"visitors"`
}

// posthogOverviewTotals soma pageviews/visitantes únicos numa janela de
// `days` dias terminando `offsetDays` dias atrás (offset=0 → período atual;
// offset=days → período anterior de mesmo tamanho, pra calcular o delta%).
func (s *Server) posthogOverviewTotals(ctx context.Context, days, offsetDays int) (postHogTotals, error) {
	q := fmt.Sprintf(`
		SELECT count() as pageviews, count(distinct person_id) as visitors
		FROM events
		WHERE event = '$pageview'
		  AND timestamp >= now() - INTERVAL %d DAY
		  AND timestamp < now() - INTERVAL %d DAY
	`, days+offsetDays, offsetDays)
	rows, err := s.posthog.query(ctx, q)
	if err != nil || len(rows) == 0 {
		return postHogTotals{}, err
	}
	return postHogTotals{Pageviews: toInt(rows[0][0]), Visitors: toInt(rows[0][1])}, nil
}

// GET /analytics/overview?range=7d|30d|90d
// Devolve os totais do período atual e do período anterior de mesmo tamanho
// (pra tela calcular o delta%, mesmo padrão do BlogMetricsOverview).
func (s *Server) handlePostHogOverview(w http.ResponseWriter, r *http.Request) {
	if s.posthogUnavailable(w) {
		return
	}
	days, err := posthogRangeDays(r.URL.Query().Get("range"))
	if err != nil {
		writeErr(w, err)
		return
	}
	curr, err := s.posthogOverviewTotals(r.Context(), days, 0)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "POSTHOG_ERROR", "Erro ao consultar a PostHog"))
		return
	}
	prev, err := s.posthogOverviewTotals(r.Context(), days, days)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "POSTHOG_ERROR", "Erro ao consultar a PostHog"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pageviews":     curr.Pageviews,
		"visitors":      curr.Visitors,
		"prevPageviews": prev.Pageviews,
		"prevVisitors":  prev.Visitors,
	})
}

// GET /analytics/timeseries?range=7d|30d|90d
// Pageviews por dia — alimenta o gráfico de área da Visão geral.
func (s *Server) handlePostHogTimeseries(w http.ResponseWriter, r *http.Request) {
	if s.posthogUnavailable(w) {
		return
	}
	days, err := posthogRangeDays(r.URL.Query().Get("range"))
	if err != nil {
		writeErr(w, err)
		return
	}
	q := fmt.Sprintf(`
		SELECT toDate(timestamp) as day, count() as pageviews
		FROM events
		WHERE event = '$pageview' AND timestamp >= now() - INTERVAL %d DAY
		GROUP BY day
		ORDER BY day
	`, days)
	rows, err := s.posthog.query(r.Context(), q)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "POSTHOG_ERROR", "Erro ao consultar a PostHog"))
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{"bucket": toStr(row[0]), "pageviews": toInt(row[1])})
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /analytics/top-pages?range=7d|30d|90d
// Ranking de páginas ($pathname) por pageviews.
func (s *Server) handlePostHogTopPages(w http.ResponseWriter, r *http.Request) {
	if s.posthogUnavailable(w) {
		return
	}
	days, err := posthogRangeDays(r.URL.Query().Get("range"))
	if err != nil {
		writeErr(w, err)
		return
	}
	q := fmt.Sprintf(`
		SELECT properties.$pathname as path, count() as views
		FROM events
		WHERE event = '$pageview' AND timestamp >= now() - INTERVAL %d DAY
		GROUP BY path
		ORDER BY views DESC
		LIMIT 20
	`, days)
	s.writePostHogCounts(w, r, q)
}

// GET /analytics/referrers?range=7d|30d|90d
// Ranking de domínios de origem do tráfego. "$direct" (visita sem referrer,
// ex.: digitou a URL ou veio de app nativo) vira "Direto" pro admin entender.
func (s *Server) handlePostHogReferrers(w http.ResponseWriter, r *http.Request) {
	if s.posthogUnavailable(w) {
		return
	}
	days, err := posthogRangeDays(r.URL.Query().Get("range"))
	if err != nil {
		writeErr(w, err)
		return
	}
	q := fmt.Sprintf(`
		SELECT if(properties.$referring_domain = '$direct' OR properties.$referring_domain = '', 'Direto', properties.$referring_domain) as referrer, count() as views
		FROM events
		WHERE event = '$pageview' AND timestamp >= now() - INTERVAL %d DAY
		GROUP BY referrer
		ORDER BY views DESC
		LIMIT 20
	`, days)
	s.writePostHogCounts(w, r, q)
}

// GET /analytics/by-site?range=7d|30d|90d
// Compara os 3 sites do ecossistema que mandam evento pro mesmo projeto
// PostHog — distinguidos pelo prefixo do path, já que os 3 são servidos sob
// santos-tech.com (mesmo $host).
func (s *Server) handlePostHogBySite(w http.ResponseWriter, r *http.Request) {
	if s.posthogUnavailable(w) {
		return
	}
	days, err := posthogRangeDays(r.URL.Query().Get("range"))
	if err != nil {
		writeErr(w, err)
		return
	}
	q := fmt.Sprintf(`
		SELECT
			multiIf(
				startsWith(properties.$pathname, '/blog'), 'Blog',
				startsWith(properties.$pathname, '/dashboard'), 'Dashboard',
				'Site principal'
			) as site,
			count() as views
		FROM events
		WHERE event = '$pageview' AND timestamp >= now() - INTERVAL %d DAY
		GROUP BY site
		ORDER BY views DESC
	`, days)
	s.writePostHogCounts(w, r, q)
}

// posthogBreakdownFields mapeia o parâmetro público "by" pra uma expressão
// HogQL fixa — nunca aceita o nome de propriedade vindo direto da query
// string, pra não abrir brecha de injeção.
var posthogBreakdownFields = map[string]string{
	"device":     "properties.$device_type",
	"country":    "properties.$geoip_country_code", // ISO alpha-2 — front usa pra mostrar a bandeira
	"browser":    "properties.$browser",
	"utm_source": "properties.utm_source", // sem "$" — utm_* são propriedades regulares, não built-in do posthog-js
}

// GET /analytics/breakdown?by=device|country|browser|utm_source&range=7d|30d|90d
// Ranking de pageviews por dispositivo/país/navegador/origem de campanha.
func (s *Server) handlePostHogBreakdown(w http.ResponseWriter, r *http.Request) {
	if s.posthogUnavailable(w) {
		return
	}
	days, err := posthogRangeDays(r.URL.Query().Get("range"))
	if err != nil {
		writeErr(w, err)
		return
	}
	field, ok := posthogBreakdownFields[r.URL.Query().Get("by")]
	if !ok {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "by inválido (use device, country, browser ou utm_source)"))
		return
	}
	q := fmt.Sprintf(`
		SELECT coalesce(nullIf(toString(%s), ''), 'Desconhecido') as label, count() as views
		FROM events
		WHERE event = '$pageview' AND timestamp >= now() - INTERVAL %d DAY
		GROUP BY label
		ORDER BY views DESC
		LIMIT 20
	`, field, days)
	s.writePostHogCounts(w, r, q)
}

// GET /analytics/recordings?range=7d|30d|90d&limit=
// Lista gravações de sessão (replay) dos 3 sites, mais recentes primeiro.
func (s *Server) handlePostHogRecordings(w http.ResponseWriter, r *http.Request) {
	if s.posthogUnavailable(w) {
		return
	}
	days, err := posthogRangeDays(r.URL.Query().Get("range"))
	if err != nil {
		writeErr(w, err)
		return
	}
	limit := clampLimit(r.URL.Query().Get("limit"), 30, 100)
	body, err := s.posthog.listRecordings(r.Context(), days, limit)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "POSTHOG_ERROR", "Erro ao consultar a PostHog"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// GET /analytics/recordings/{id}/events
// Devolve os eventos rrweb já descompactados de uma gravação — pronto pro
// rrweb-player tocar. Pode demorar alguns segundos (busca e descompacta todos
// os blobs da sessão) e a resposta pode chegar a alguns MB numa sessão longa.
func (s *Server) handlePostHogRecordingEvents(w http.ResponseWriter, r *http.Request) {
	if s.posthogUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	events, err := s.posthog.recordingEvents(r.Context(), id)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "POSTHOG_ERROR", "Erro ao consultar a PostHog"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// writePostHogCounts executa uma HogQL de 2 colunas (label, count) e devolve
// no shape {key,count}[] — mesmo formato do BlogMetricsCount, reaproveitado
// pelo RankingCard do front.
func (s *Server) writePostHogCounts(w http.ResponseWriter, r *http.Request, hogql string) {
	rows, err := s.posthog.query(r.Context(), hogql)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "POSTHOG_ERROR", "Erro ao consultar a PostHog"))
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{"key": toStr(row[0]), "count": toInt(row[1])})
	}
	writeJSON(w, http.StatusOK, out)
}

// toInt converte um valor JSON genérico (a PostHog devolve números como
// float64 via encoding/json) pra int, sem pânico se o shape vier diferente.
func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

// toStr converte um valor JSON genérico pra string, sem pânico se vier nil
// ou um tipo inesperado.
func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}
