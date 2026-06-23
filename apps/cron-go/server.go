package main

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/santos-tech/cron-go/db"
	"github.com/santos-tech/golog"
)

type Server struct {
	cfg Config
	db  *pgxpool.Pool
	q   *db.Queries
}

func NewServer(cfg Config, pool *pgxpool.Pool) *Server {
	return &Server{cfg: cfg, db: pool, q: db.New(pool)}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.Handle("GET /metrics", promhttp.Handler())
	// rotas /cron/* adicionadas nas Tasks 4–9
	mux.HandleFunc("GET /cron/catalog", s.handleListCatalog) // TODO(Task 6): embrulhar com s.requireAdmin
	return golog.RequestLogger(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "Postgres indisponível")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
