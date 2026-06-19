package main

import (
	"context"
	"net/http"
)

// analyticsSource isola a fonte do analytics (o *Store em prod, um fake nos testes).
type analyticsSource interface {
	GetAnalytics(ctx context.Context, r AnalyticsRange) (Analytics, error)
}

func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	data, err := s.analytics.GetAnalytics(r.Context(), parseRange(r.URL.Query().Get("range")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao calcular analytics")
		return
	}
	writeJSON(w, http.StatusOK, data)
}
