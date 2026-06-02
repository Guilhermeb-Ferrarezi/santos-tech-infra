package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type ctxKey string

const userIDKey ctxKey = "userID"

type Server struct {
	cfg  Config
	db   *pgxpool.Pool
	rdb  *redis.Client
	mgr  *SessionManager
	auth *claudeAuth
}

func NewServer(cfg Config, db *pgxpool.Pool, rdb *redis.Client) *Server {
	s := &Server{cfg: cfg, db: db, rdb: rdb}
	s.mgr = newSessionManager(s)
	s.auth = newClaudeAuth(cfg)
	return s
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /claude/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	s.registerRoutes(mux)
	return s.cors(s.globalRateLimit(mux))
}

// ── CORS (com credenciais) ───────────────────────────────────────────────────

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (len(s.cfg.CORSOrigins) == 0 || slices.Contains(s.cfg.CORSOrigins, origin)) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		w.Header().Add("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── Auth guard (admin-only) ──────────────────────────────────────────────────
// Valida o JWT do auth central (cookie access_token ou Authorization: Bearer) e
// exige role=Admin, pois o serviço roda Claude com --dangerously-skip-permissions.

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
		uid, err := s.authenticate(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey, uid)))
	}
}

// authenticate valida o token e o papel admin, retornando o userID. Reusável
// fora do middleware (ex: upgrade de WebSocket).
func (s *Server) authenticate(r *http.Request) (int64, error) {
	token := bearerToken(r)
	if token == "" {
		return 0, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Não autenticado")
	}
	uid, err := verifyToken(token, s.cfg.JWTSecret)
	if err != nil {
		return 0, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado")
	}
	role, err := s.userRole(r.Context(), uid)
	if err != nil {
		return 0, err
	}
	if role != RoleAdmin {
		return 0, appErr(http.StatusForbidden, "FORBIDDEN", "Acesso restrito a administradores")
	}
	return uid, nil
}

func userIDFrom(r *http.Request) int64 {
	v, _ := r.Context().Value(userIDKey).(int64)
	return v
}

func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
