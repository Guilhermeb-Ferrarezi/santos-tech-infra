package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/santos-tech/golog"
)

const version = "0.1.0"

type Server struct {
	cfg     Config
	client  *apiClient
	fetch   *http.Client  // busca imagens de URLs do usuário (anti-SSRF; trocável em teste)
	openapi []byte        // docs/openapi.yaml carregado no boot (vazio = resource indisponível)
	rdb     *redis.Client // opcional; nil = ban check desabilitado
	printer *bambuClient  // opcional; nil = tool bambu_status desabilitada
}

func NewServer(cfg Config, openapi []byte, rdb *redis.Client) *Server {
	s := &Server{cfg: cfg, client: newAPIClient(), fetch: newFetchClient(), openapi: openapi, rdb: rdb}
	if cfg.BambuUserID != "" && cfg.BambuAccessToken != "" && cfg.BambuDeviceID != "" {
		s.printer = newBambuClient(cfg)
		if token := s.printer.client.Connect(); token.WaitTimeout(10*time.Second) && token.Error() != nil {
			slog.Warn("bambu mqtt: falha ao conectar no boot, vai tentar de novo sozinho", "err", token.Error())
		}
	}
	return s
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
		case "/health", "/ready", "/metrics", "/mcp/health", "/mcp/ready":
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

// MCP monta o servidor MCP com todas as tools e resources.
func (s *Server) MCP() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "santos-tech",
		Title:   "Santos Tech — ecossistema",
		Version: version,
	}, nil)
	s.addAuthTools(srv)
	s.addEmailTools(srv)
	s.addClaudeTools(srv)
	s.addUploadTools(srv)
	s.addBotTools(srv)
	s.addPaymentsTools(srv)
	s.addPrinterTools(srv)
	s.addWorkspaceTools(srv)
	s.addContentTools(srv)
	s.addAdminTools(srv)
	s.addResources(srv)
	return srv
}

// Handler expõe GET /health (sem auth) e o endpoint MCP (Streamable HTTP) na
// raiz, exigindo Authorization. Stateless: cada chamada é independente — não
// guardamos sessão MCP, então qualquer réplica atende qualquer request.
func (s *Server) Handler() http.Handler {
	srv := s.MCP()
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	mux := http.NewServeMux()
	health := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":%q}`, version)
	}
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /mcp/health", health) // Traefik roteia /mcp sem strip

	// /ready (público) testa a dependência downstream (a API central): sem ela
	// este gateway não serve as tools. Não há Postgres/Redis aqui para pingar.
	mux.HandleFunc("GET /ready", s.ready)
	mux.HandleFunc("GET /mcp/ready", s.ready)

	// /metrics (público) expõe os coletores Prometheus.
	mux.Handle("GET /metrics", promhttp.Handler())

	// Metadata RFC 9728 — clientes OAuth (claude.ai) descobrem por aqui qual
	// authorization server protege este resource. Público e cross-origin.
	prm := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		fmt.Fprintf(w, `{"resource":%q,"authorization_servers":[%q],"bearer_methods_supported":["header"]}`,
			s.cfg.PublicURL, originOf(s.cfg.PublicURL))
	}
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", prm)
	mux.HandleFunc("GET /mcp/.well-known/oauth-protected-resource", prm)

	mux.Handle("/", s.requireAuth(streamable))
	return golog.RequestLogger(s.ipBanCheck(metricsMiddleware(mux)))
}

// ready pinga a API central (AuthBaseURL) com timeout curto: se ela não responder,
// o gateway não tem o que servir, então devolve 503. /health continua respondendo
// 200 (liveness) — só /ready reflete a dependência.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.cfg.AuthBaseURL == "" {
		w.Write([]byte(`{"status":"ok"}`)) // sem downstream configurado: nada a checar
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.AuthBaseURL+"/health", nil)
	if err == nil {
		var resp *http.Response
		resp, err = s.client.http.Do(req)
		if resp != nil {
			defer resp.Body.Close()
			if resp.StatusCode >= 500 {
				err = fmt.Errorf("auth api HTTP %d", resp.StatusCode)
			}
		}
	}
	if err != nil {
		// Não expõe err.Error() (pode vazar URL/infra) num endpoint público.
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"unavailable","dependency":"auth-api"}`))
		return
	}
	w.Write([]byte(`{"status":"ok"}`))
}

// originOf extrai scheme://host de uma URL (fallback: a própria string).
func originOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return rawURL
	}
	return u.Scheme + "://" + u.Host
}

// requireAuth barra requests sem credencial antes de tocarem o MCP. O token em
// si não é validado aqui — a primeira chamada à API de destino valida (e é ela
// quem conhece PAT, JWT, papéis e sudo). O 401 aponta o resource_metadata
// (RFC 9728) para clientes OAuth iniciarem o fluxo de autorização.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	// URL path-inserted da RFC 9728: origem + /.well-known/... + path do resource.
	resourceURL, _ := url.Parse(s.cfg.PublicURL)
	metadataURL := s.cfg.PublicURL
	if resourceURL != nil && resourceURL.Host != "" {
		metadataURL = originOf(s.cfg.PublicURL) + "/.well-known/oauth-protected-resource" + resourceURL.Path
	}
	challenge := fmt.Sprintf(`Bearer realm="santos-tech", resource_metadata=%q`, metadataURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 8<<20) // 8MB de teto no request
		if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
			w.Header().Set("WWW-Authenticate", challenge)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"code":"UNAUTHORIZED","message":"Envie Authorization: Bearer <PAT st_... ou JWT do auth central>."}`)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authorization extrai o header Authorization do request MCP (o SDK propaga os
// headers HTTP em Extra.Header).
func authorization(extra *mcp.RequestExtra) string {
	if extra != nil && extra.Header != nil {
		return extra.Header.Get("Authorization")
	}
	return ""
}

// Prazos por chamada: o padrão cobre as APIs CRUD; geração via Claude Agent
// pode levar até 90s no destino, então ganha folga.
const (
	defaultCallTimeout  = 30 * time.Second
	generateCallTimeout = 110 * time.Second
)

// proxy chama a API de destino repassando o Authorization do request MCP e
// converte a resposta em resultado de tool: 2xx vira texto (JSON cru da API),
// 4xx/5xx vira isError com o corpo original ({code,message} ou {error}).
func (s *Server) proxy(ctx context.Context, req *mcp.CallToolRequest, method, url string, body any) (*mcp.CallToolResult, any, error) {
	return s.proxyTimeout(ctx, req, method, url, body, defaultCallTimeout)
}

func (s *Server) proxyTimeout(ctx context.Context, req *mcp.CallToolRequest, method, url string, body any, timeout time.Duration) (*mcp.CallToolResult, any, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	status, raw, err := s.client.do(ctx, method, url, authorization(req.Extra), body)
	return resultFrom(method, url, status, raw, err)
}

// proxyBot chama o dashboard API do bot-go com a DASH key de serviço (não o token
// do usuário). Usado pelas tools read-only de agendamentos/leads/conversas.
func (s *Server) proxyBot(ctx context.Context, method, url string, body any) (*mcp.CallToolResult, any, error) {
	if s.cfg.BotAPIURL == "" || s.cfg.BotDashKey == "" {
		return errResult("ferramenta indisponível: BOT_API_URL/BOT_DASH_KEY não configurados neste MCP."), nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	status, raw, err := s.client.doBot(ctx, method, url, s.cfg.BotDashKey, body)
	return resultFrom(method, url, status, raw, err)
}

// resultFrom converte a resposta de uma API downstream em resultado de tool.
func resultFrom(method, url string, status int, raw []byte, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return errResult(fmt.Sprintf("API indisponível (%s %s): %v", method, url, err)), nil, nil
	}
	if status >= 400 {
		errBody := strings.TrimSpace(string(raw))
		if len(errBody) > 2000 {
			errBody = errBody[:2000] + "…"
		}
		msg := fmt.Sprintf("HTTP %d: %s", status, errBody)
		if strings.Contains(errBody, `"SUDO_REQUIRED"`) || strings.Contains(errBody, `"sudo_required"`) {
			msg += " — a ação exige modo sudo (confirmação recente de identidade em auth.santos-tech.com/confirm)."
		}
		return errResult(msg), nil, nil
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		text = `{"ok":true}` // 204 e afins
	}
	return textResult(text), nil, nil
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}
