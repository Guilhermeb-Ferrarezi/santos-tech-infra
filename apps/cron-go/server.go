package main

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/santos-tech/golog"
)

type Server struct {
	cfg Config
	db  *pgxpool.Pool
	// q *db.Queries  // adicionado na Task 3
}

func NewServer(cfg Config, pool *pgxpool.Pool) *Server {
	return &Server{cfg: cfg, db: pool}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.Handle("GET /metrics", promhttp.Handler())
	// rotas /cron/* adicionadas nas Tasks 4–9
	return golog.RequestLogger(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2_000_000_000) // 2s
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "Postgres indisponível")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
