package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// handleCoolifyWebhook recebe eventos de deploy da Coolify e envia WhatsApp
// conforme o status do evento (falha, sucesso, serviço fora do ar).
// URL: POST /webhooks/coolify?token=<COOLIFY_WEBHOOK_SECRET>
func (s *Server) handleCoolifyWebhook(w http.ResponseWriter, r *http.Request) {
	if secret := s.cfg.CoolifyWebhookSecret; secret != "" {
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(secret)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64_000))
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	status := coolifyDeployStatus(payload)
	if status == "" || status == "started" {
		// Sem classificação acionável (ou apenas "iniciou") → nada a notificar.
		w.WriteHeader(http.StatusOK)
		return
	}

	go s.sendCoolifyNotif(status, payload)

	w.WriteHeader(http.StatusOK)
}

// coolifyDeployStatus classifica o evento da Coolify num status normalizado.
// Retorna "failed" | "success" | "unhealthy" | "started" | "" (desconhecido).
// É defensiva com os vários formatos: campos de topo `status`/`type`/`message`,
// e o sub-objeto `deployment`.
func coolifyDeployStatus(p map[string]any) string {
	// Coleta os textos candidatos de status/type em vários lugares do payload.
	var signals []string
	add := func(v any) {
		if s, ok := v.(string); ok && s != "" {
			signals = append(signals, strings.ToLower(s))
		}
	}
	add(p["status"])
	add(p["type"])
	add(p["event"])
	add(p["state"])
	add(p["message"])
	if dep, ok := p["deployment"].(map[string]any); ok {
		add(dep["status"])
		add(dep["state"])
	}
	if data, ok := p["data"].(map[string]any); ok {
		add(data["status"])
		add(data["state"])
		add(data["type"])
	}

	// Ordem importa: falha tem prioridade sobre o resto.
	for _, sig := range signals {
		if strings.Contains(sig, "fail") || strings.Contains(sig, "error") {
			return "failed"
		}
	}
	for _, sig := range signals {
		if strings.Contains(sig, "unhealthy") || strings.Contains(sig, "stopped") ||
			strings.Contains(sig, "exited") || strings.Contains(sig, "down") ||
			strings.Contains(sig, "crashed") {
			return "unhealthy"
		}
	}
	for _, sig := range signals {
		if strings.Contains(sig, "success") || strings.Contains(sig, "finished") ||
			strings.Contains(sig, "deployed") || strings.Contains(sig, "healthy") ||
			strings.Contains(sig, "running") {
			return "success"
		}
	}
	for _, sig := range signals {
		if strings.Contains(sig, "start") || strings.Contains(sig, "queued") ||
			strings.Contains(sig, "progress") || strings.Contains(sig, "building") {
			return "started"
		}
	}
	return ""
}

// coolifyAppName extrai o nome da aplicação de forma defensiva.
func coolifyAppName(p map[string]any) string {
	for _, k := range []string{"application_name", "name", "service_name", "project_name"} {
		if v, ok := p[k].(string); ok && v != "" {
			return v
		}
	}
	if dep, ok := p["deployment"].(map[string]any); ok {
		for _, k := range []string{"application_name", "name"} {
			if v, ok := dep[k].(string); ok && v != "" {
				return v
			}
		}
	}
	if app, ok := p["application"].(map[string]any); ok {
		if v, ok := app["name"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// coolifyString busca a primeira chave não-vazia (string) no payload e, opcionalmente,
// no sub-objeto `deployment`.
func coolifyString(p map[string]any, keys ...string) string {
	lookup := func(m map[string]any) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	if v := lookup(p); v != "" {
		return v
	}
	if dep, ok := p["deployment"].(map[string]any); ok {
		if v := lookup(dep); v != "" {
			return v
		}
	}
	return ""
}

// shortSHA encurta um hash de commit para 7 caracteres (se parecer um SHA).
func shortSHA(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// statusHeading devolve o título da mensagem por status normalizado.
func statusHeading(status string) string {
	switch status {
	case "failed":
		return "⚠️ Deploy falhou"
	case "success":
		return "✅ Deploy concluído"
	case "unhealthy":
		return "🔴 Serviço fora do ar"
	default:
		return "ℹ️ Atualização de deploy"
	}
}

func (s *Server) sendCoolifyNotif(status string, payload map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var phone, instance string
	var enabled, onSuccess, onContainerDown bool
	err := s.pool.QueryRow(ctx,
		`SELECT notif_phone, notif_instance, notif_enabled, notif_on_success, notif_on_container_down
		   FROM tenant_config WHERE tenant_id = $1`,
		s.cfg.TenantID,
	).Scan(&phone, &instance, &enabled, &onSuccess, &onContainerDown)
	if err != nil || !enabled || phone == "" || instance == "" {
		return
	}

	// Gate por tipo de evento. Falha é sempre notificada quando enabled.
	switch status {
	case "failed":
		// sempre
	case "success":
		if !onSuccess {
			return
		}
	case "unhealthy":
		if !onContainerDown {
			return
		}
	default:
		return
	}

	if s.cfg.EvolutionAPIURL == "" || s.cfg.EvolutionAPIKey == "" {
		slog.Error("coolify notif: Evolution API não configurada")
		return
	}

	appName := coolifyAppName(payload)

	// Anti-spam: não repete a mesma (app+status) numa janela curta.
	if !s.notifAllow(ctx, appName, status, 60*time.Second) {
		return
	}

	// O webhook não traz commit/autor; busca via API da Coolify pelo deployment_uuid.
	if du := coolifyString(payload, "deployment_uuid", "deployment_id", "deployment"); du != "" {
		cool := NewCoolifyClient(s.cfg.CoolifyAPIURL, s.cfg.CoolifyAPIToken)
		if cool.Enabled() {
			if commit, cmsg, e := cool.DeploymentInfo(ctx, du); e == nil {
				if commit != "" {
					payload["commit"] = commit
				}
				if cmsg != "" {
					payload["commit_message"] = cmsg
				}
			}
		}
	}

	text := s.buildCoolifyMessage(status, appName, payload)

	evo := NewEvolutionClient(s.cfg.EvolutionAPIURL, s.cfg.EvolutionAPIKey, instance)
	if err := evo.SendText(ctx, phone, text); err != nil {
		slog.Error("coolify notif: falha ao enviar WhatsApp", "err", err, "phone", phone)
	}
}

// buildCoolifyMessage monta a mensagem enriquecida com o que o payload trouxer.
func (s *Server) buildCoolifyMessage(status, appName string, payload map[string]any) string {
	var b strings.Builder
	b.WriteString(statusHeading(status))
	if appName != "" {
		b.WriteString("\nAplicação: ")
		b.WriteString(appName)
	}

	if branch := coolifyString(payload, "git_branch", "branch", "ref"); branch != "" {
		b.WriteString("\nBranch: ")
		b.WriteString(branch)
	}
	if commit := coolifyString(payload, "commit", "commit_sha", "git_commit_sha", "sha"); commit != "" {
		b.WriteString("\nCommit: ")
		b.WriteString(shortSHA(commit))
		if cm := coolifyString(payload, "commit_message"); cm != "" {
			if i := strings.IndexByte(cm, '\n'); i >= 0 {
				cm = cm[:i] // só a primeira linha
			}
			b.WriteString(" — ")
			b.WriteString(strings.TrimSpace(cm))
		}
	}
	if url := coolifyString(payload, "deployment_url", "log_url", "url", "link"); url != "" {
		b.WriteString("\nLog: ")
		b.WriteString(url)
	}
	if msg := coolifyString(payload, "message"); msg != "" {
		b.WriteString("\n")
		b.WriteString(msg)
	}
	return b.String()
}

// ── Anti-spam (dedupe) ─────────────────────────────────────────────────────

// notifAllow retorna true se a notificação (app+status) pode ser enviada agora,
// e false se uma idêntica já saiu dentro da janela `window`. Usa Redis (SET NX EX)
// quando disponível; cai pro dedupe in-memory se o Redis falhar/estiver ausente.
func (s *Server) notifAllow(ctx context.Context, appName, status string, window time.Duration) bool {
	key := fmt.Sprintf("bot:coolify-notif:%s:%s:%s", s.cfg.TenantID, status, appName)
	if s.rdb != nil {
		ok, err := s.rdb.SetNX(ctx, key, "1", window).Result()
		if err == nil {
			return ok
		}
		// Erro no Redis → não bloqueia a notificação; usa o fallback in-memory.
		slog.Warn("coolify notif: dedupe via Redis falhou, usando fallback", "err", err)
	}
	if s.notifDedupe == nil {
		return true
	}
	return s.notifDedupe.allow(key, window)
}

// notifDedupe é o fallback in-memory (quando não há Redis acessível).
type notifDedupe struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newNotifDedupe() *notifDedupe {
	return &notifDedupe{seen: make(map[string]time.Time)}
}

// allow retorna true e marca a chave se ela não foi vista dentro da janela.
func (d *notifDedupe) allow(key string, window time.Duration) bool {
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	// Limpeza oportunista das entradas expiradas.
	for k, t := range d.seen {
		if now.Sub(t) > window {
			delete(d.seen, k)
		}
	}
	if t, ok := d.seen[key]; ok && now.Sub(t) <= window {
		return false
	}
	d.seen[key] = now
	return true
}
