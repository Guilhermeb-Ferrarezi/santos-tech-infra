package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/santos-tech/golog"
)

// Server agrupa as dependências do secrets-go: config, Postgres (só para
// requireAdmin — ver main.go), Redis opcional (só para ipBanCheck — ver
// ratelimit.go) e as peças do domínio de scan portadas do dotfy-scanner
// (crawler, state, elastic, hub de SSE, revalidator).
type Server struct {
	cfg Config
	db  *pgxpool.Pool
	rdb *redis.Client // opcional; nil quando REDIS_URL não está configurada

	crawler     *Crawler
	state       *StateManager
	es          *ElasticClient
	hub         *Hub
	revalidator *Revalidator
}

func NewServer(cfg Config, db *pgxpool.Pool, rdb *redis.Client, crawler *Crawler, state *StateManager, es *ElasticClient, hub *Hub, revalidator *Revalidator) *Server {
	return &Server{
		cfg:         cfg,
		db:          db,
		rdb:         rdb,
		crawler:     crawler,
		state:       state,
		es:          es,
		hub:         hub,
		revalidator: revalidator,
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ready", s.handleReady)  // público: pinga Postgres + Elasticsearch
	mux.Handle("GET /metrics", metricsHandler()) // público: Prometheus

	// Todas as rotas de domínio exigem admin autenticado (role==3 no Postgres
	// compartilhado) — dado sensível (chaves reais de terceiros + PII), sem
	// exceção nenhuma, nem para /events (SSE).
	//
	// Sem prefixo /api aqui: o Traefik do Coolify já aplica um middleware
	// stripprefix pro path do app (/secrets) antes de encaminhar pro
	// container — mesmo padrão de payments-go (registra "/students", não
	// "/payments/students"). O frontend chama authApi("/secrets/status"), o
	// Traefik entrega só "/status" aqui.
	mux.HandleFunc("/status", s.requireAdmin(s.handleStatus))
	mux.HandleFunc("/start", s.requireAdmin(s.handleStart))
	mux.HandleFunc("/stop", s.requireAdmin(s.handleStop))
	mux.HandleFunc("/settings", s.requireAdmin(s.handleSettings))
	mux.HandleFunc("/keywords", s.requireAdmin(s.handleKeywords))
	mux.HandleFunc("/keywords/stats", s.requireAdmin(s.handleKeywordStats))
	mux.HandleFunc("/repos", s.requireAdmin(s.handleRepos))
	mux.HandleFunc("/clear", s.requireAdmin(s.handleClear))
	mux.HandleFunc("/revalidate", s.requireAdmin(s.handleRevalidate))
	mux.HandleFunc("/events", s.requireAdmin(s.hub.ServeSSE))

	return golog.RequestLogger(s.cors(s.ipBanCheck(metricsMiddleware(mux))))
}

// handleReady verifica as dependências (Postgres e Elasticsearch) com timeout
// curto. Responde 503 se alguma falhar — usado pelo readiness probe.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if s.db != nil {
		if err := s.db.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "dependency": "postgres"})
			return
		}
	}
	if s.es != nil {
		if err := s.es.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "dependency": "elasticsearch"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// cors reflete a origem só se ela estiver na allowlist (CORS_ORIGIN, CSV) —
// nunca reflete qualquer origem fora da lista (regra de segurança do CLAUDE.md).
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── Auth ──────────────────────────────────────────────────────────────────

type ctxKey string

const userIDKey ctxKey = "uid"

// bearerToken extrai o token do cookie access_token ou do header Authorization: Bearer.
func bearerToken(r *http.Request) string {
	if c, err := r.Cookie("access_token"); err == nil && c.Value != "" {
		return c.Value
	}
	if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return after
	}
	return ""
}

func (s *Server) authGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if tok == "" {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Não autenticado")
			return
		}
		uid, err := verifyToken(tok, s.cfg.JWTSecret)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Não autenticado")
			return
		}
		golog.SetUserID(r.Context(), uid)
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey, uid)))
	}
}

// requireAdmin exige token válido E role Admin (users.role == 3) no Postgres
// compartilhado — SEM fallback nenhum de permissão de cargo (diferente de
// payments-go): dado sensível (chaves reais de terceiros + PII), só role==3
// libera, sempre.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		uid := r.Context().Value(userIDKey).(int64)
		var role int
		var name string
		err := s.db.QueryRow(r.Context(), `SELECT role, name FROM users WHERE id=$1`, uid).Scan(&role, &name)
		if err != nil || role != 3 {
			writeError(w, http.StatusForbidden, "forbidden", "Acesso restrito a administradores")
			return
		}
		golog.SetUserName(r.Context(), name)
		next(w, r)
	})
}
