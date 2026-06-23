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
	cfg        Config
	db         *pgxpool.Pool
	q          *db.Queries
	authClient *http.Client
	// hostCheck é o seam de teste: por padrão delega para hostAllowed com a
	// allowlist de produção. Testes podem sobrescrever para bypassar a checagem
	// sem afrouxar o guard de produção.
	hostCheck func(host string) bool
}

func NewServer(cfg Config, pool *pgxpool.Pool) *Server {
	s := &Server{
		cfg:        cfg,
		db:         pool,
		q:          db.New(pool),
		authClient: &http.Client{Timeout: 5 * time.Second},
	}
	s.hostCheck = func(host string) bool {
		return hostAllowed(host, s.cfg.HostAllowlist)
	}
	return s
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.Handle("GET /metrics", promhttp.Handler())
	// rotas /cron/* — todas protegidas por requireAdmin
	mux.HandleFunc("GET /cron/catalog", s.requireAdmin(s.handleListCatalog))
	mux.HandleFunc("POST /cron/preview", s.requireAdmin(s.handlePreview))
	mux.HandleFunc("GET /cron/jobs", s.requireAdmin(s.handleListJobs))
	mux.HandleFunc("POST /cron/jobs", s.requireAdmin(s.handleCreateJob))
	mux.HandleFunc("GET /cron/jobs/{id}", s.requireAdmin(s.handleGetJob))
	mux.HandleFunc("PATCH /cron/jobs/{id}", s.requireAdmin(s.handleUpdateJob))
	mux.HandleFunc("DELETE /cron/jobs/{id}", s.requireAdmin(s.handleDeleteJob))
	mux.HandleFunc("POST /cron/jobs/{id}/pause", s.requireAdmin(s.handlePauseJob))
	mux.HandleFunc("POST /cron/jobs/{id}/resume", s.requireAdmin(s.handleResumeJob))
	mux.HandleFunc("POST /cron/jobs/{id}/run", s.requireAdmin(s.handleRunJob))
	mux.HandleFunc("GET /cron/jobs/{id}/runs", s.requireAdmin(s.handleListRuns))
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
