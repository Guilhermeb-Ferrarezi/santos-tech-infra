package main

import (
	"net/http"
	"strings"
	"time"
)

// ── Ingestão (público, sem auth) ────────────────────────────────────────────

func validateBlogEventInput(in *BlogEventInput) error {
	in.Type = strings.TrimSpace(in.Type)
	in.Path = strings.TrimSpace(in.Path)
	in.SessionID = strings.TrimSpace(in.SessionID)
	in.VisitorID = strings.TrimSpace(in.VisitorID)
	if !validBlogEventTypes[in.Type] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "type inválido")
	}
	if in.Path == "" || len(in.Path) > 512 {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "path inválido")
	}
	if in.SessionID == "" || len(in.SessionID) > 128 {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "sessionId inválido")
	}
	if in.VisitorID == "" || len(in.VisitorID) > 128 {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "visitorId inválido")
	}
	if in.PostSlug != nil {
		slug := strings.TrimSpace(*in.PostSlug)
		if slug == "" {
			in.PostSlug = nil
		} else {
			in.PostSlug = &slug
		}
	}
	return nil
}

// POST /public/blog/events
func (s *Server) handleBlogEventIngest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10) // 4KB — beacon é pequeno
	var in BlogEventInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateBlogEventInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	country := r.Header.Get("CF-IPCountry")
	if err := s.insertBlogEvent(r.Context(), in, r.UserAgent(), country); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Agregação (admin — blog_posts:read) ─────────────────────────────────────

// blogMetricsParamsFrom lê from/to (YYYY-MM-DD) e postSlug opcional da
// querystring. "to" é exclusivo e vira o FIM do dia informado (23:59:59.999...)
// pra incluir o dia inteiro.
func blogMetricsParamsFrom(r *http.Request) (BlogMetricsFilter, error) {
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	if fromStr == "" || toStr == "" {
		return BlogMetricsFilter{}, appErr(http.StatusBadRequest, "BAD_REQUEST", "from e to são obrigatórios")
	}
	from, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		return BlogMetricsFilter{}, appErr(http.StatusBadRequest, "BAD_REQUEST", "from inválido (use YYYY-MM-DD)")
	}
	to, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		return BlogMetricsFilter{}, appErr(http.StatusBadRequest, "BAD_REQUEST", "to inválido (use YYYY-MM-DD)")
	}
	to = to.Add(24 * time.Hour) // exclusivo: inclui o dia "to" inteiro
	if !to.After(from) {
		return BlogMetricsFilter{}, appErr(http.StatusBadRequest, "BAD_REQUEST", "to deve ser depois de from")
	}
	f := BlogMetricsFilter{From: from, To: to}
	if slug := strings.TrimSpace(r.URL.Query().Get("postSlug")); slug != "" {
		f.PostSlug = &slug
	}
	return f, nil
}

// GET /blog/metrics/overview
func (s *Server) handleBlogMetricsOverview(w http.ResponseWriter, r *http.Request) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogMetricsOverview(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /blog/metrics/timeseries
func (s *Server) handleBlogMetricsTimeseries(w http.ResponseWriter, r *http.Request) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogMetricsTimeseries(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /blog/metrics/top-posts
func (s *Server) handleBlogMetricsTopPosts(w http.ResponseWriter, r *http.Request) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogMetricsTopPosts(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /blog/metrics/referrers
func (s *Server) handleBlogMetricsReferrers(w http.ResponseWriter, r *http.Request) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogMetricsReferrers(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /blog/metrics/utm-source
func (s *Server) handleBlogMetricsUTMSource(w http.ResponseWriter, r *http.Request) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogMetricsUTMSource(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /blog/metrics/devices
func (s *Server) handleBlogMetricsDevices(w http.ResponseWriter, r *http.Request) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogMetricsDevices(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /blog/metrics/countries
func (s *Server) handleBlogMetricsCountries(w http.ResponseWriter, r *http.Request) {
	f, err := blogMetricsParamsFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.blogMetricsCountries(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
