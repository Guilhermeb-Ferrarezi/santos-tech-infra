package main

import (
	"net/http"
	"strconv"
	"strings"
)

// ── Ingestão (público, sem auth) ────────────────────────────────────────────

func validateHeatmapBatch(in *BlogHeatmapBatch) error {
	in.PostSlug = strings.TrimSpace(in.PostSlug)
	in.Viewport = strings.TrimSpace(in.Viewport)
	in.SessionID = strings.TrimSpace(in.SessionID)

	if in.PostSlug == "" || len(in.PostSlug) > 200 {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "postSlug inválido")
	}
	if !validHeatmapViewports[in.Viewport] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "viewport inválido (use mobile ou desktop)")
	}
	if in.SessionID == "" || len(in.SessionID) > 128 {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "sessionId inválido")
	}
	if len(in.Clicks) > heatmapMaxClicksPerBatch {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "lote de cliques grande demais")
	}
	for _, c := range in.Clicks {
		if c.XPct < 0 || c.XPct > 1 || c.YPct < 0 || c.YPct > 1 {
			return appErr(http.StatusBadRequest, "BAD_REQUEST", "clique fora do intervalo esperado")
		}
	}
	if in.MaxScrollDepthPct != nil && (*in.MaxScrollDepthPct < 0 || *in.MaxScrollDepthPct > 1) {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "maxScrollDepthPct fora do intervalo esperado")
	}
	if len(in.Clicks) == 0 && in.MaxScrollDepthPct == nil {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "lote vazio")
	}
	return nil
}

// POST /public/blog/heatmap/events
func (s *Server) handleBlogHeatmapIngest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10) // 16KB — lote de até 200 cliques cabe folgado
	var in BlogHeatmapBatch
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateHeatmapBatch(&in); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.insertHeatmapBatch(r.Context(), in); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Agregação (admin — blog_posts:read) ─────────────────────────────────────

// blogHeatmapParamsFrom reaproveita o from/to de blogMetricsParamsFrom, mas
// aqui postSlug (já parseado por ela) e viewport são OBRIGATÓRIOS — heatmap
// de clique/scroll só faz sentido pra um post e um bucket de viewport por vez,
// diferente dos outros endpoints de /blog/metrics que aceitam "todos os posts".
func blogHeatmapParamsFrom(r *http.Request) (string, string, BlogMetricsFilter, error) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		return "", "", BlogMetricsFilter{}, err
	}
	if f.PostSlug == nil || *f.PostSlug == "" {
		return "", "", BlogMetricsFilter{}, appErr(http.StatusBadRequest, "BAD_REQUEST", "postSlug é obrigatório")
	}
	viewport := strings.TrimSpace(r.URL.Query().Get("viewport"))
	if !validHeatmapViewports[viewport] {
		return "", "", BlogMetricsFilter{}, appErr(http.StatusBadRequest, "BAD_REQUEST", "viewport inválido (use mobile ou desktop)")
	}
	return *f.PostSlug, viewport, f, nil
}

// gridDimFromQuery lê cols/rows da querystring — o admin calcula os dois a
// partir do tamanho REAL do post já renderizado (altura/largura medida ÷
// células de ~35px), então a grade acompanha a variação de altura entre
// posts em vez de usar um número fixo que fica grosso demais em posts muito
// compridos. Ausente/inválido cai no default; sempre saturado pelos limites.
func gridDimFromQuery(r *http.Request, name string, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return clampHeatmapGridSize(n)
}

// GET /blog/metrics/heatmap/clicks
func (s *Server) handleBlogHeatmapClicks(w http.ResponseWriter, r *http.Request) {
	postSlug, viewport, f, err := blogHeatmapParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	cols := gridDimFromQuery(r, "cols", heatmapDefaultCols)
	gridRows := gridDimFromQuery(r, "rows", heatmapDefaultRows)
	out, err := s.blogHeatmapClickGrid(r.Context(), postSlug, viewport, cols, gridRows, f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /blog/metrics/heatmap/scroll
func (s *Server) handleBlogHeatmapScroll(w http.ResponseWriter, r *http.Request) {
	postSlug, viewport, f, err := blogHeatmapParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogHeatmapScrollFunnel(r.Context(), postSlug, viewport, f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
