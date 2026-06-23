package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/santos-tech/agent/db"
	"github.com/santos-tech/golog"
)

type ctxKey string

const userIDKey ctxKey = "userID"

type Server struct {
	cfg  Config
	db   *pgxpool.Pool
	q    *db.Queries
	rdb  *redis.Client
	mgr  *SessionManager
	auth *claudeAuth
}

func NewServer(cfg Config, pool *pgxpool.Pool, rdb *redis.Client) *Server {
	s := &Server{cfg: cfg, db: pool, q: db.New(pool), rdb: rdb}
	s.mgr = newSessionManager(s)
	s.auth = newClaudeAuth(cfg)
	return s
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	// Endpoints públicos (sem authGuard nem rate limit) — usados pelo healthcheck
	// e pelo scrape do Prometheus.
	mux.HandleFunc("GET /claude/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /claude/ready", s.handleReady)
	mux.Handle("GET /claude/metrics", metricsHandler())
	s.registerRoutes(mux)
	return golog.RequestLogger(metricsMiddleware(s.cors(s.ipBanCheck(s.globalRateLimit(mux)))))
}

// handleReady é o readiness probe: pinga Redis e Postgres com timeout curto e
// responde 503 se qualquer dependência falhar. Público (sem authGuard) para não
// quebrar o healthcheck.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	checks := map[string]string{"redis": "ok", "postgres": "ok"}
	ready := true
	if err := s.rdb.Ping(ctx).Err(); err != nil {
		checks["redis"] = "unavailable"
		ready = false
	}
	if err := s.db.Ping(ctx); err != nil {
		checks["postgres"] = "unavailable"
		ready = false
	}
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{"ready": ready, "checks": checks})
}

// ── CORS (com credenciais) ───────────────────────────────────────────────────

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		// Fail-closed: com allowlist vazia, só reflete origin (com credenciais) em dev.
		allowed := slices.Contains(s.cfg.CORSOrigins, origin)
		if len(s.cfg.CORSOrigins) == 0 && !s.cfg.Production {
			allowed = true
		}
		if origin != "" && allowed {
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
		golog.SetUserID(r.Context(), uid)
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey, uid)))
	}
}

// authGuardUser autentica qualquer usuário válido (JWT de sessão ou PAT) SEM exigir
// admin. Usado apenas pelo /claude/generate, que é sandbox (sem tools, --add-dir,
// MCP, --dangerously-skip-permissions ou tokens de infra) — então não precisa do
// papel admin que protege as rotas privilegiadas de conversa.
func (s *Server) authGuardUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Não autenticado"))
			return
		}
		// Serviços internos (ex: bot-atendimento) usam INTERNAL_SECRET como Bearer.
		// (#5) Comparação em tempo constante: o `==` de strings retorna no 1º byte
		// diferente, abrindo um canal de timing para adivinhar o segredo byte a byte.
		if s.cfg.InternalSecret != "" &&
			subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.InternalSecret)) == 1 {
			next(w, r.WithContext(context.WithValue(r.Context(), userIDKey, int64(0))))
			return
		}
		uid, err := s.resolveToken(r.Context(), token)
		if err != nil {
			writeErr(w, err)
			return
		}
		golog.SetUserID(r.Context(), uid)
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
	uid, err := s.resolveToken(r.Context(), token)
	if err != nil {
		return 0, err
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

// resolveToken aceita um JWT de sessão ou um Personal Access Token (prefixo "st_",
// validado contra a tabela api_keys compartilhada com o auth), devolvendo o userID.
// A exigência de papel admin continua sendo feita em authenticate.
func (s *Server) resolveToken(ctx context.Context, token string) (int64, error) {
	if strings.HasPrefix(token, "st_") {
		uid, err := s.userIDByAPIKeyHash(ctx, sha256Hex(token))
		if err != nil {
			return 0, err
		}
		if uid == 0 {
			return 0, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado")
		}
		return uid, nil
	}
	uid, err := verifyToken(token, s.cfg.JWTSecret)
	if err != nil {
		return 0, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado")
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
