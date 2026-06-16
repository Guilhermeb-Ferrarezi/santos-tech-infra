package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type Server struct {
	cfg      Config
	db       *pgxpool.Pool
	rdb      *redis.Client
	store    *Store
	provider PaymentProvider
	email    *emailClient
}

func NewServer(cfg Config, db *pgxpool.Pool, rdb *redis.Client, provider PaymentProvider) *Server {
	return &Server{
		cfg:      cfg,
		db:       db,
		rdb:      rdb,
		store:    &Store{db: db},
		provider: provider,
		email:    newEmailClient(cfg),
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("POST /students", s.requireAdmin(s.handleCreateStudent))
	mux.HandleFunc("GET /students", s.requireAdmin(s.handleListStudents))
	mux.HandleFunc("GET /students/{id}", s.requireAdmin(s.handleGetStudent))
	mux.HandleFunc("GET /students/{id}/charges", s.requireAdmin(s.handleStudentCharges))

	mux.HandleFunc("POST /plans", s.requireAdmin(s.handleCreatePlan))
	mux.HandleFunc("GET /plans", s.requireAdmin(s.handleListPlans))

	mux.HandleFunc("POST /subscriptions", s.requireAdmin(s.handleCreateSubscription))
	mux.HandleFunc("GET /subscriptions", s.requireAdmin(s.handleListSubscriptions))
	mux.HandleFunc("PATCH /subscriptions/{id}", s.requireAdmin(s.handlePatchSubscription))

	mux.HandleFunc("POST /charges", s.requireAdmin(s.handleCreateCharge))
	mux.HandleFunc("GET /charges", s.requireAdmin(s.handleListCharges))
	mux.HandleFunc("GET /charges/{id}", s.requireAdmin(s.handleGetCharge))

	mux.HandleFunc("POST /webhooks/dotfy", s.handleDotfyWebhook)

	return s.cors(mux)
}

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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
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
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey, uid)))
	}
}

// requireAdmin exige token válido E role Admin (users.role == 3) no Postgres compartilhado.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		uid := r.Context().Value(userIDKey).(int64)
		var role int
		err := s.db.QueryRow(r.Context(), `SELECT role FROM users WHERE id=$1`, uid).Scan(&role)
		if err != nil || role != 3 {
			writeError(w, http.StatusForbidden, "forbidden", "Acesso restrito a administradores")
			return
		}
		next(w, r)
	})
}
