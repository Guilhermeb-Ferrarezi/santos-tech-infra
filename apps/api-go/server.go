package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/santos-tech/auth/db"
	"github.com/santos-tech/golog"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type ctxKey string

const userIDKey ctxKey = "userID"

type Server struct {
	cfg Config
	db  *pgxpool.Pool
	// q: camada de acesso ao banco via sqlc (gerado em db/). Todas as queries
	// estáticas do auth (users, sessions, api_keys, etc.) migram para cá.
	// O pool s.db continua necessário para Begin/Ping/Close.
	q *db.Queries
	// portalDB: pool do banco do domínio do portal (/portal/*). Hoje aponta pra
	// um banco separado do auth; os handlers de portal usam ESTE pool, enquanto
	// auth/guards/users seguem no `db`. Se não houver banco separado, é o mesmo
	// pool de `db` (ver wiring no main).
	portalDB *pgxpool.Pool
	rdb      *redis.Client
	email    *emailClient
	google   *oauth2.Config
	r2       *R2           // uploads (Cloudflare R2); nil = desabilitado
	loki     *lokiClient   // consulta de logs (Loki); nil = desabilitado
	queue    *asynq.Client // fila durável de emails; nil = sem fila (fallback fire-and-forget)
}

func NewServer(cfg Config, authDB, portalDB *pgxpool.Pool, rdb *redis.Client) *Server {
	s := &Server{cfg: cfg, db: authDB, q: db.New(authDB), portalDB: portalDB, rdb: rdb, email: newEmailClient(cfg), r2: newR2(cfg), loki: newLokiClient(cfg.LokiURL)}
	if s.portalDB == nil {
		s.portalDB = authDB
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
	// Endpoints operacionais PÚBLICOS (sem auth, fora do rate limit): liveness,
	// readiness e métricas. Necessário para healthcheck/scrape não quebrarem.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.Handle("GET /metrics", metricsHandler())
	registerPoolMetrics(s.db)
	s.registerAuthRoutes(mux)
	// metricsMiddleware fica DENTRO do mux (após o roteamento) para enxergar o
	// padrão de rota casado (r.Pattern) e evitar explosão de cardinalidade.
	// antiBotCheck fica DEPOIS do ipBanCheck (um IP já banido nem chega aqui) e
	// ANTES do globalRateLimit (ver antibot.go).
	return golog.RequestLogger(s.cors(s.ipBanCheck(s.antiBotCheck(s.globalRateLimit(metricsMiddleware(mux))))))
}

// ── CORS (com credenciais, igual ao Fastify) ─────────────────────────────────

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.allowedOrigin(origin) {
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

// allowedOrigin reporta se origin está na allowlist explícita.
// Fail-closed: lista vazia bloqueia todas as origens.
func (s *Server) allowedOrigin(origin string) bool {
	if slices.Contains(s.cfg.CORSOrigins, origin) {
		return true
	}
	return s.cfg.AuthWebOrigin != "" && origin == s.cfg.AuthWebOrigin
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
		uid, u, err := s.resolveToken(r.Context(), token)
		if err != nil {
			writeErr(w, err)
			return
		}
		golog.SetUserID(r.Context(), uid)
		if u != nil {
			golog.SetUserName(r.Context(), u.Name)
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey, uid)))
	}
}

// adminGuard exige sessão válida E role de administrador. Reaproveita authGuard
// para resolver o token e injetar o userID; depois carrega o usuário e checa o role.
func (s *Server) adminGuard(next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.cachedUserByID(r.Context(), userIDFrom(r))
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
// (prefixo "st_"), devolvendo o userID e, para JWT, o *User já carregado do banco
// (evitando um segundo SELECT no caller que precisar do perfil completo).
// Para PATs o *User retornado é sempre nil — o caller busca quando necessário.
// Erros já vêm como *AppError prontos para writeErr; falha de banco vira 500.
func (s *Server) resolveToken(ctx context.Context, token string) (int64, *User, error) {
	if strings.HasPrefix(token, "st_") {
		uid, err := s.userIDByAPIKeyHash(ctx, sha256Hex(token))
		if err != nil {
			return 0, nil, err
		}
		if uid == 0 {
			return 0, nil, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado")
		}
		return uid, nil, nil
	}
	uid, _, err := verifyToken(token, s.cfg.JWTSecret)
	if err != nil {
		return 0, nil, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado")
	}
	// O access-token JWT é curto mas sobrevive à suspensão (a sessão/refresh é
	// apagada, mas o JWT já emitido continua válido até expirar). Conferimos o
	// estado atual do dono no banco para que suspender/desabilitar corte o acesso
	// de imediato, sem esperar o JWT vencer. Sem banco (modo degradado/teste) não
	// há como checar suspensão: o JWT já é criptograficamente válido, então segue.
	if s.db == nil {
		return uid, nil, nil
	}
	u, err := s.cachedUserByID(ctx, uid)
	if err != nil {
		return 0, nil, err
	}
	if u == nil || u.SuspendedAt != nil || u.LoginDisabled {
		return 0, nil, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado")
	}
	return uid, u, nil
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
