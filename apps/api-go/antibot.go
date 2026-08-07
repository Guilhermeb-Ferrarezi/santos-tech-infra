package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/santos-tech/auth/db"
)

// Este arquivo implementa detecção automática de bots de varredura, alimentando
// o mesmo sistema de ip-ban que já existe (Postgres + Redis, ver ratelimit.go e
// handlers_admin_ip_bans.go) — não é um sistema paralelo. Motivado por um
// incidente real em produção (2026-08-06): o IP 35.224.247.89 disparou 221
// requisições numa janela de minutos, forjando o User-Agent do crawler Diffbot
// enquanto varria .env/.git/credenciais de cloud/config de agentes de IA
// (.claude.json, .cursor/mcp.json…). O rate-limit global (200/min) chegou a
// bloquear parte das tentativas (429), mas ninguém baniu o IP — ele volta a
// tentar assim que a janela reseta. As três camadas abaixo fecham esse buraco:
//
//  1. honeypotPaths: paths que NENHUM cliente real desta API jamais pede (não
//     colidem com nenhuma rota de routes.go). Um hit é 100% doloso → ban
//     imediato. É a camada de menor risco de falso positivo.
//  2. badUASignatures: User-Agents que se identificam como ferramenta de scan
//     conhecida. Barato, mas trivialmente contornável (o próprio incidente
//     acima já usava UA forjado) — tratar como bônus, não defesa primária.
//  3. check404Burst: muitos 404 do mesmo IP numa janela curta → ban temporário.
//     Pega scanners em paths fora do honeypot, mas tem risco real de falso
//     positivo (IP compartilhado/NAT batendo endpoint removido por engano) —
//     por isso o limiar é bem acima do que um usuário real geraria.
//
// Todo ban automático usa autoBanTTL (finito) — diferente do ban manual do
// admin, que pode ser permanente — para não banir pra sempre um IP
// dinâmico/compartilhado por causa de uma heurística. Reversível a qualquer
// momento via DELETE /auth/admin/ip-bans/{id} (o ban aparece na listagem normal
// com reason preenchido e bannedBy nulo).

const (
	autoBanTTL = 24 * time.Hour

	notFoundBurstMax    = 30
	notFoundBurstWindow = time.Minute
)

// antibotBansTotal conta bans automáticos por motivo, exposto em /metrics.
var antibotBansTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "antibot_bans_total",
	Help: "Total de IPs banidos automaticamente pelo antibot, por motivo.",
}, []string{"reason"})

// honeypotPaths: curado a partir do tráfego de scan real observado em produção
// (2026-08-06) mais assinaturas clássicas de reconhecimento. Deliberadamente
// SEM artefatos que um navegador comum pode pedir sozinho (favicon.ico,
// manifest.webmanifest, sw.js, service-worker.js, ngsw.json) e sem paths
// ambíguos que poderiam ser erro de digitação humano (/dashboard, /info,
// /_health) — só entra aqui o que é inequivocamente varredura de segredo/CVE.
var honeypotPaths = buildHoneypotPaths()

func buildHoneypotPaths() map[string]bool {
	groups := [][]string{
		// VCS / CI
		{
			"/.git/HEAD", "/.git/config", "/.git-credentials", "/.gitconfig",
			"/.gitlab-ci.yml", "/.github/.env", "/.github/workflows/deploy.yml",
			"/.aider.conf.yml",
		},
		// Shell / dotfiles de segredo
		{
			"/.bash_profile", "/.bashrc", "/.zshrc", "/.pypirc",
			"/.streamlit/secrets.toml",
		},
		// Config de agentes de IA / editores — alvo novo e notável: o scanner do
		// incidente procurava explicitamente credenciais do Claude Code.
		{
			"/.claude.json", "/.claude/settings.json", "/.codex/config.toml",
			"/.cursor/mcp.json", "/.mcp.json", "/.continue/config.json",
			"/.config/anthropic/credentials/default.json", "/.openclaw/.env",
			"/.openclaw/openclaw.json", "/.hermes/.env", "/.hermes/auth.json",
			"/.hermes/config.yaml", "/.vscode/launch.json",
		},
		// Família .env (raiz e prefixada por diretório)
		{
			"/.env", "/.env.backup", "/.env.bak", "/.env.dev", "/.env.development",
			"/.env.docker", "/.env.example", "/.env.local", "/.env.old",
			"/.env.prod.bak", "/.env.production", "/.env.production.bak",
			"/.env.staging", "/.env.swp", "/.env.test",
			"/admin/.env", "/api/.env", "/app/.env", "/backend/.env",
			"/config/.env", "/dev/.env", "/docker/.env", "/frontend/.env",
			"/production/.env", "/public/.env", "/server/.env", "/src/.env",
			"/staging/.env", "/web/.env", "/@fs/.env", "/@fs/root/.env",
		},
		// Credenciais de cloud (AWS/GCP/Firebase)
		{
			"/.aws/config", "/.aws/credentials", "/gcp-credentials.json",
			"/gcp-key.json", "/gcp-service.json", "/google-credentials.json",
			"/google-service-account.json", "/google-services.json",
			"/firebase-admin.json", "/firebase-adminsdk.json",
			"/firebase-config.json", "/firebase-credentials.json",
			"/firebase-service-account.json", "/firebase.json",
			"/service-account.json", "/serviceAccountKey.json",
			"/service_account.json", "/sa.json", "/gc-service.json",
			"/keys/service-account.json", "/config/firebase-admin.json",
			"/config/gcp-credentials.json", "/__/firebase/init.json",
		},
		// Chaves SSH / TLS
		{
			"/.ssh/id_rsa", "/.ssh/known_hosts", "/id_rsa", "/id_dsa",
			"/id_ecdsa", "/id_ed25519", "/host.key", "/key.pem",
			"/private-key", "/privatekey.key", "/server.key", "/localhost.key",
			"/ssl/localhost.key", "/ssl/server.key",
		},
		// Config/segredos de app e framework
		{
			"/app-config.json", "/app/settings.py", "/application.properties",
			"/application.yml", "/appsettings.json", "/appsettings.Development.json",
			"/appsettings.Production.json", "/backend/settings.py",
			"/bootstrap.properties", "/bootstrap.yml", "/config.env", "/config.js",
			"/config.json", "/config.py", "/config.toml", "/config.yaml",
			"/config/application.properties", "/config/database.yml",
			"/config/master.key", "/config/runtime.exs", "/config/secrets.yml",
			"/config/settings.py", "/config/storage.yml", "/configuration.js",
			"/configuration.php.bak", "/core/settings.py", "/credentials.json",
			"/docker-compose.yaml", "/Dockerfile", "/gradle.properties",
			"/.gradle/gradle.properties", "/instance/config.py", "/key.json",
			"/local.settings.json", "/push_config.json", "/rclone.conf",
			"/runtime-config.js", "/runtime.js", "/secrets.json", "/secrets.yml",
			"/sendgrid.env", "/settings.json", "/settings.py", "/web.config",
			"/env.js", "/env.json", "/__env.js", "/public/admin.json",
		},
		// Artefatos PHP e debug de framework
		{
			"/phpinfo.php", "/info.php", "/test.php", "/i.php", "/pi.php",
			"/app_dev.php", "/app_dev.php/_profiler", "/_ignition/health-check",
			"/_profiler/latest", "/_profiler/open", "/_debugbar/open",
			"/elmah.axd", "/trace.axd",
		},
		// Actuator / telescope (Spring/Laravel introspection)
		{
			"/actuator", "/actuator/configprops", "/actuator/env",
			"/actuator/mappings", "/telescope/requests",
		},
		// Sondagem de API/GraphQL genérica
		{
			"/api/account", "/api/config", "/api/env", "/api/graphql",
			"/api/openapi.json", "/api/settings", "/api/v1/config",
			"/api/v1/env", "/api/v1/settings", "/api/v2/config",
			"/api/v2/settings", "/graphql", "/graphql/console", "/v1/graphql",
			"/openapi.json",
		},
		// WordPress
		{"/wp-json", "/wp-json/gravitysmtp/v1/tests/mock-data"},
		// Introspecção de servidor
		{"/server-status", "/server-info", "/nginx_status"},
	}

	out := make(map[string]bool)
	for _, g := range groups {
		for _, p := range g {
			out[p] = true
		}
	}
	return out
}

// badUASignatures: substrings (case-insensitive) que identificam ferramenta de
// scan conhecida. Curta e específica de propósito — termos genéricos (curl,
// python, go-http-client) teriam falso positivo alto demais: APIs legítimas do
// próprio ecossistema usam essas libs (ex.: python-httpx no refresh de OAuth
// do MCP).
var badUASignatures = []string{
	"sqlmap", "nikto", "nmap", "masscan", "zgrab", "nuclei", "nessus",
	"acunetix", "wpscan", "dirbuster", "gobuster", "vuln_scanner",
}

func isBadUA(ua string) bool {
	lower := strings.ToLower(ua)
	for _, sig := range badUASignatures {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// statusCapture captura o status HTTP escrito pelo handler, só pra decidir se
// conta como 404 na heurística de burst. Sem passthrough de Flusher/Hijacker:
// api-go não tem rota SSE/WebSocket (diferente do agent-go/mcp-go).
type statusCapture struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusCapture) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCapture) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

// antiBotCheck: honeypot + UA ruim ANTES do handler (resposta idêntica a um 404
// normal, pra não denunciar a armadilha); burst de 404 é medido DEPOIS,
// olhando o status real da resposta. Fica entre ipBanCheck (um IP já banido nem
// chega aqui) e globalRateLimit no chain de server.go.
func (s *Server) antiBotCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/ready", "/metrics":
			next.ServeHTTP(w, r)
			return
		}

		ip := clientIP(r)

		if honeypotPaths[r.URL.Path] {
			s.autoBan(r.Context(), ip, "honeypot: "+r.URL.Path, "honeypot")
			http.NotFound(w, r)
			return
		}

		if ua := r.UserAgent(); isBadUA(ua) {
			s.autoBan(r.Context(), ip, "user-agent: "+ua, "bad_ua")
			http.NotFound(w, r)
			return
		}

		sc := &statusCapture{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sc, r)

		if sc.status == http.StatusNotFound {
			s.check404Burst(r.Context(), ip)
		}
	})
}

// check404Burst bane o IP quando ele acumula muitos 404 numa janela curta.
// Reaproveita s.allow (mesmo contador do rate-limit) com fail-OPEN: uma falha
// do Redis não deve gerar ban (a resposta já foi enviada de qualquer forma).
// Depois do primeiro ban, as próximas requisições desse IP são cortadas mais
// cedo por ipBanCheck — então isso só dispara uma vez por incidente.
func (s *Server) check404Burst(ctx context.Context, ip string) {
	key := "api-go:antibot:404burst:" + ip
	if s.allow(ctx, key, notFoundBurstMax, notFoundBurstWindow, false) {
		return
	}
	reason := fmt.Sprintf("mais de %d respostas 404 em %s", notFoundBurstMax, notFoundBurstWindow)
	s.autoBan(ctx, ip, reason, "404burst")
}

// autoBan bane um IP sem intervenção humana: grava no Redis pra efeito
// imediato (mesma chave "global:ip-ban:<ip>" que ipBanCheck consulta) e
// persiste no Postgres via UpsertIPBan (bannedBy NULL = ban do sistema) pra
// sobreviver a um restart (ver syncBansToRedis). TTL finito (autoBanTTL),
// diferente do ban manual do admin — evita banir pra sempre um IP
// dinâmico/compartilhado por falso positivo de heurística.
func (s *Server) autoBan(ctx context.Context, ip, reason, metricReason string) {
	slog.Warn("antibot: ban automático", "ip", ip, "reason", reason)
	antibotBansTotal.WithLabelValues(metricReason).Inc()

	if err := s.rdb.Set(ctx, "global:ip-ban:"+ip, "1", autoBanTTL).Err(); err != nil {
		slog.Warn("antibot: falha ao banir no Redis", "ip", ip, "err", err)
	}

	if s.q == nil {
		return // sem banco (modo degradado/teste) — ban fica só no Redis
	}
	expiresAt := pgtype.Timestamptz{Time: time.Now().Add(autoBanTTL), Valid: true}
	if _, err := s.q.UpsertIPBan(ctx, db.UpsertIPBanParams{
		Ip:        ip,
		Reason:    &reason,
		ExpiresAt: expiresAt,
	}); err != nil {
		slog.Warn("antibot: falha ao persistir ban no Postgres", "ip", ip, "err", err)
	}
}
