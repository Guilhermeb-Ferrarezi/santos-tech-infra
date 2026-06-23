package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/santos-tech/cron-go/db"
	"github.com/santos-tech/golog"
)

type Server struct {
	cfg        Config
	db         *pgxpool.Pool
	q          *db.Queries
	authClient *http.Client
	rdb        *redis.Client // opcional; nil = ban check desabilitado
	// hostCheck é o seam de teste: por padrão delega para hostAllowed com a
	// allowlist de produção. Testes podem sobrescrever para bypassar a checagem
	// sem afrouxar o guard de produção.
	hostCheck func(host string) bool
	// wg rastreia os dispatches em voo para drená-los no shutdown.
	wg sync.WaitGroup
}

func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ipBanCheck bloqueia IPs banidos consultando Redis ("global:ip-ban:<ip>").
// Fail-open: Redis nil ou indisponível → passa. Endpoints operacionais são isentos.
func (s *Server) ipBanCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/ready", "/metrics":
			next.ServeHTTP(w, r)
			return
		}
		if s.rdb != nil {
			if n, err := s.rdb.Exists(r.Context(), "global:ip-ban:"+clientIP(r)).Result(); err == nil && n > 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"code":"FORBIDDEN","message":"Acesso não autorizado."}`))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// WaitDrain aguarda os dispatches em voo terminarem, até `timeout`. Chamado no
// shutdown depois que o scheduler parou de reivindicar novos jobs.
func (s *Server) WaitDrain(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

func NewServer(cfg Config, pool *pgxpool.Pool, rdb *redis.Client) *Server {
	s := &Server{
		cfg:        cfg,
		db:         pool,
		q:          db.New(pool),
		authClient: &http.Client{Timeout: 5 * time.Second},
		rdb:        rdb,
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
	// Rotas da API admin — registradas SEM o prefixo /cron porque o Traefik da
	// Coolify roteia api.santos-tech.com/cron → este serviço STRIPANDO o /cron
	// (mesma convenção do payments-go em /payments). Externamente as rotas são
	// /cron/jobs etc.; aqui chegam bare (/jobs…). Todas protegidas por requireAdmin.
	mux.HandleFunc("GET /catalog", s.requireAdmin(s.handleListCatalog))
	mux.HandleFunc("POST /preview", s.requireAdmin(s.handlePreview))
	mux.HandleFunc("GET /jobs", s.requireAdmin(s.handleListJobs))
	mux.HandleFunc("POST /jobs", s.requireAdmin(s.handleCreateJob))
	mux.HandleFunc("GET /jobs/{id}", s.requireAdmin(s.handleGetJob))
	mux.HandleFunc("PATCH /jobs/{id}", s.requireAdmin(s.handleUpdateJob))
	mux.HandleFunc("DELETE /jobs/{id}", s.requireAdmin(s.handleDeleteJob))
	mux.HandleFunc("POST /jobs/{id}/pause", s.requireAdmin(s.handlePauseJob))
	mux.HandleFunc("POST /jobs/{id}/resume", s.requireAdmin(s.handleResumeJob))
	mux.HandleFunc("POST /jobs/{id}/run", s.requireAdmin(s.handleRunJob))
	mux.HandleFunc("GET /jobs/{id}/runs", s.requireAdmin(s.handleListRuns))
	return golog.RequestLogger(s.ipBanCheck(s.cors(mux)))
}

// cors ecoa a Origin se estiver na allowlist (com credentials) e responde ao
// preflight OPTIONS. Mesma convenção do payments-go/api-go.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		for _, o := range s.cfg.CORSOrigins {
			if o == origin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				break
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
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
