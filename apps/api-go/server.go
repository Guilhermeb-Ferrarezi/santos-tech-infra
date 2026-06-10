package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type ctxKey string

const userIDKey ctxKey = "userID"

type Server struct {
	cfg Config
	db  *pgxpool.Pool
	// portalDB: pool do banco do domínio do portal (/portal/*). Hoje aponta pra
	// um banco separado do auth; os handlers de portal usam ESTE pool, enquanto
	// auth/guards/users seguem no `db`. Se não houver banco separado, é o mesmo
	// pool de `db` (ver wiring no main).
	portalDB *pgxpool.Pool
	rdb      *redis.Client
	email    *emailClient
	google   *oauth2.Config
	r2       *R2 // uploads (Cloudflare R2); nil = desabilitado
}

func NewServer(cfg Config, db, portalDB *pgxpool.Pool, rdb *redis.Client) *Server {
	s := &Server{cfg: cfg, db: db, portalDB: portalDB, rdb: rdb, email: newEmailClient(cfg), r2: newR2(cfg)}
	if s.portalDB == nil {
		s.portalDB = db
	}
	if cfg.GoogleClientID != "" {
		s.google = &oauth2.Config{
			ClientID:     cfg.GoogleClientID,
			ClientSecret: cfg.GoogleClientSecret,
			RedirectURL:  cfg.GoogleCallbackURL,
			Scopes:       []string{"email", "profile"},
			Endpoint:     google.Endpoint,
		}
	}
	return s
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	s.registerAuthRoutes(mux)
	return s.cors(s.globalRateLimit(mux))
}

// ── CORS (com credenciais, igual ao Fastify) ─────────────────────────────────

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
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── Auth guard (cookie access_token ou Authorization: Bearer) ────────────────

func (s *Server) authGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if c, err := r.Cookie("access_token"); err == nil {
			token = c.Value
		}
		if token == "" {
			if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
				token = after
			}
		}
		if token == "" {
			writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Não autenticado"))
			return
		}
		uid, err := s.resolveToken(r.Context(), token)
		if err != nil {
			writeErr(w, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey, uid)))
	}
}

// adminGuard exige sessão válida E role de administrador. Reaproveita authGuard
// para resolver o token e injetar o userID; depois carrega o usuário e checa o role.
func (s *Server) adminGuard(next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.userByID(r.Context(), userIDFrom(r))
		if err != nil {
			writeErr(w, err)
			return
		}
		if u == nil || u.Role != RoleAdmin {
			writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "Acesso restrito a administradores"))
			return
		}
		next(w, r)
	})
}

// resolveToken aceita tanto um JWT de sessão quanto um Personal Access Token
// (prefixo "st_"), devolvendo o userID. Erros já vêm como *AppError prontos para
// writeErr; uma falha de banco no caminho do PAT vira 500.
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

// ── Cookies ──────────────────────────────────────────────────────────────────

func (s *Server) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Domain:   s.cfg.CookieDomain,
		HttpOnly: true,
		Secure:   s.cfg.Production,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

func (s *Server) setAuthCookies(w http.ResponseWriter, access, refresh string) {
	s.setCookie(w, "access_token", access, int(accessTTL.Seconds()))
	s.setCookie(w, "refresh_token", refresh, int(refreshTTL.Seconds()))
}

func (s *Server) clearAuthCookies(w http.ResponseWriter) {
	s.setCookie(w, "access_token", "", -1)
	s.setCookie(w, "refresh_token", "", -1)
}

func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
