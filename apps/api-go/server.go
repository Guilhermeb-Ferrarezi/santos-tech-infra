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
	portalDB  *pgxpool.Pool
	rdb       *redis.Client
	email     *emailClient
	google    *oauth2.Config
	r2        *R2              // uploads (Cloudflare R2); nil = desabilitado
	drive     *DriveClient     // pastas de arquivos no Google Drive (Arquivos); nil = desabilitado
	loki      *lokiClient      // consulta de logs (Loki); nil = desabilitado
	sentry    *sentryClient    // consulta de issues (Sentry); nil = desabilitado
	posthog   *posthogClient   // consulta HogQL (PostHog); nil = desabilitado
	queue     *asynq.Client    // fila durável de emails; nil = sem fila (fallback fire-and-forget)
	instagram *instagramClient // private reply automática (comentário -> DM) + publicação de conteúdo; enabled()=false = desabilitado
	facebook  *facebookClient  // publicação automática na Página (ver social_publish.go); enabled()=false = desabilitado
	vault     *Vault           // cifra as chaves do roteador de APIs; nil = feature desabilitada (endpoints respondem 503)
	// feriadosNacionais: cliente da BrasilAPI (feriados nacionais, cache 24h) — ver agenda_feriados.go.
	feriadosNacionais *feriadosNacionaisClient
}

func NewServer(cfg Config, authDB, portalDB *pgxpool.Pool, rdb *redis.Client) *Server {
	s := &Server{cfg: cfg, db: authDB, q: db.New(authDB), portalDB: portalDB, rdb: rdb, email: newEmailClient(cfg), r2: newR2(cfg), drive: newDriveClient(cfg), loki: newLokiClient(cfg.LokiURL), sentry: newSentryClient(cfg.SentryOrgSlug, cfg.SentryToken), posthog: newPostHogClient(cfg.PostHogHost, cfg.PostHogProjectID, cfg.PostHogAPIKey), instagram: newInstagramClient(cfg), facebook: newFacebookClient(cfg), vault: newVault(cfg.VaultSecret, cfg.VaultSalt), feriadosNacionais: newFeriadosNacionaisClient()}
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
	// Endpoints operacionais fora do rate limit / ip-ban / anti-bot (probes e
	// scrape são frequentes): liveness, readiness e métricas. /health e /ready
	// são públicos; /metrics exige `Authorization: Bearer $METRICS_TOKEN`
	// quando a env está definida (ver metricsHandler).
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
	return golog.RequestLogger(securityHeaders(s.cors(s.ipBanCheck(s.antiBotCheck(s.globalRateLimit(metricsMiddleware(mux)))))))
}

// ── Cabeçalhos de segurança ──────────────────────────────────────────────────

// securityHeaders adiciona cabeçalhos de segurança HTTP a todas as respostas
// (portado de dashboard/api/internal/httpapi/middleware.go). Fica FORA do cors
// para cobrir também o 204 do preflight.
//
// Os headers neutros vão sempre. Já `X-Frame-Options: DENY` e a CSP restritiva
// só são aplicados quando a resposta NÃO é um arquivo servido inline (imagem,
// vídeo, áudio, PDF de /drive-folders/{id}/files/{fileId}/download): nesses
// casos eles quebrariam o preview no navegador. A proteção contra XSS
// armazenado nesse caminho continua sendo a allowlist de tipos inline
// (isInlineSafeContentType) + nosniff + fallback para attachment.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Permitted-Cross-Domain-Policies", "none")
		h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=()")
		sw := &securityHeadersWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		// Handler que não escreveu nada: o net/http ainda vai emitir 200 —
		// aplica os headers restantes agora.
		sw.applyFramingHeaders()
	})
}

// securityHeadersWriter aplica os headers dependentes do Content-Type no
// momento em que o status é escrito (depois disso o mapa de headers é ignorado).
type securityHeadersWriter struct {
	http.ResponseWriter
	applied bool
}

// Unwrap permite que http.ResponseController alcance o writer original
// (Flush/Hijack/SetWriteDeadline continuam funcionando para SSE e streaming).
func (w *securityHeadersWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *securityHeadersWriter) applyFramingHeaders() {
	if w.applied {
		return
	}
	w.applied = true
	if isInlineFileContentType(w.Header().Get("Content-Type")) {
		return
	}
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
}

func (w *securityHeadersWriter) WriteHeader(code int) {
	w.applyFramingHeaders()
	w.ResponseWriter.WriteHeader(code)
}

func (w *securityHeadersWriter) Write(b []byte) (int, error) {
	w.applyFramingHeaders()
	return w.ResponseWriter.Write(b)
}

func (w *securityHeadersWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// isInlineFileContentType reporta se ct é um tipo de arquivo que o navegador
// renderiza direto (e que, portanto, pode ser embutido num preview) — nele não
// entram X-Frame-Options nem a CSP `default-src 'none'`.
func isInlineFileContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch {
	case strings.HasPrefix(ct, "image/"), strings.HasPrefix(ct, "video/"), strings.HasPrefix(ct, "audio/"):
		return true
	case ct == "application/pdf", ct == "application/octet-stream":
		return true
	}
	return false
}

// ── CORS (com credenciais, igual ao Fastify) ─────────────────────────────────

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		// Rotas /public/* não usam cookie/credencial (token na URL é a
		// credencial) — liberadas pra qualquer origem, sem
		// Allow-Credentials. Cobre o app desktop (Tauri: origem
		// tauri://localhost/http://tauri.localhost), que nunca vai entrar
		// na allowlist de origens do dashboard/auth-web.
		if strings.HasPrefix(r.URL.Path, "/public/") {
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			} else {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			}
		} else if origin != "" && s.allowedOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		w.Header().Add("Vary", "Origin")
		if r.Method == http.MethodOptions {
			// X-Requested-With: o @santos-techrp/account-kit manda esse header
			// (proteção anti-CSRF) em toda chamada POST/PUT/PATCH/DELETE SEM
			// corpo (needsCsrfHeader em api()/authApi()) — faltava aqui, então
			// só quebrava em rotas sem body (ex.: publish-confirmations),
			// nunca nas que mandam JSON (esse header some quando hasBody=true).
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Max-Age", "86400")
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
		// Token do /oauth/token não é sessão do painel: ele sai com
		// aud=<client_id> (ver oauthprovider.go). Atrás da flag
		// OAUTH_AUD_ENFORCE porque hoje clients e app mobile ainda usam esse
		// token nas rotas normais; o /oauth/userinfo continua aceitando (chama
		// resolveToken direto, sem passar por aqui).
		if s.cfg.OAuthAudEnforce && tokenAudience(token, s.cfg.JWTSecret) != "" {
			writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado"))
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
	uid, _, err := verifyToken(token, s.cfg.JWTSecret, tokenTypeAccess)
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

// maxJSONBody é o teto padrão do corpo JSON aceito por decodeJSON.
const maxJSONBody = 1 << 20 // 1 MB

// decodeJSON decodifica o corpo JSON da requisição com um teto EMBUTIDO de 1 MB.
// O limite é padrão, e não opt-in: antes, quem esquecesse o http.MaxBytesReader
// antes da chamada ficava exposto a DoS de memória (corpo ilimitado). Handlers
// que já limitam o corpo por conta própria continuam valendo — os MaxBytesReader
// aninham e o MENOR dos limites prevalece. Quem precisa de mais que 1 MB usa
// decodeJSONLimit.
func decodeJSON(r *http.Request, v any) error {
	return decodeJSONLimit(r, v, maxJSONBody)
}

// decodeJSONLimit é a variante explícita, para corpos legitimamente maiores que
// o padrão (cena do board, payloads de áudio em base64 do roteador de APIs).
// O w é nil de propósito: sem ele o net/http só deixa de marcar a conexão como
// "requisição grande demais", e o erro de leitura (*http.MaxBytesError) chega
// igual ao decoder.
func decodeJSONLimit(r *http.Request, v any, max int64) error {
	r.Body = http.MaxBytesReader(nil, r.Body, max)
	return json.NewDecoder(r.Body).Decode(v)
}
