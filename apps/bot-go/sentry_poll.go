package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Poller periódico que avisa por WhatsApp quando uma issue NOVA aparece no
// Sentry em qualquer um dos 14 projetos do ecossistema — sem isso, ninguém
// fica sabendo de um erro capturado a menos que abra o dashboard/sentry.io
// por conta própria. Reaproveita o mesmo destinatário/liga-desliga
// (tenant_config.notif_phone/notif_instance/notif_enabled) já usado pros
// avisos de deploy da Coolify (handlers_coolify.go).

// sentryProjectIDs mapeia cada serviço ao ID numérico do seu projeto no
// Sentry (extraído do path do DSN). Mantenha em sincronia com o mesmo mapa
// em apps/api-go/sentry_client.go se um projeto novo for criado.
var sentryProjectIDs = map[string]string{
	"api-go":        "4511887960834048",
	"agent-go":      "4511887960965120",
	"bot-go":        "4511887961030656",
	"cron-go":       "4511887961030657",
	"mcp-go":        "4511887961096192",
	"payments-go":   "4511887961161728",
	"auto-fixer":    "4511887961227264",
	"auth-web":      "4511887961292800",
	"pay-web":       "4511887961358336",
	"whats":         "4511887951003648",
	"home-page":     "4511887961423872",
	"blog-web":      "4511887961489408",
	"dashboard-web": "4511887961554944",
	"dashboard-api": "4511887961620480",
}

type sentryPollIssue struct {
	ID        string `json:"id"`
	ShortID   string `json:"shortId"`
	Title     string `json:"title"`
	Culprit   string `json:"culprit"`
	Level     string `json:"level"`
	Permalink string `json:"permalink"`
	Project   struct {
		Slug string `json:"slug"`
	} `json:"project"`
}

// startSentryPoller roda até ctx ser cancelado. No-op se SENTRY_API_TOKEN ou
// SENTRY_ORG_SLUG não estiverem configurados.
func (s *Server) startSentryPoller(ctx context.Context) {
	if s.cfg.SentryAPIToken == "" || s.cfg.SentryOrgSlug == "" {
		return
	}
	interval := s.cfg.SentryPollInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	httpClient := &http.Client{Timeout: 15 * time.Second}
	t := time.NewTicker(interval)
	defer t.Stop()

	// Primeira varredura logo no boot (não espera o 1º tick), pra pegar o
	// atraso entre reinicializações também.
	s.pollSentryOnce(ctx, httpClient, interval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.pollSentryOnce(ctx, httpClient, interval)
		}
	}
}

// pollSentryOnce busca issues cujo primeiro evento caiu na última janela
// (interval + 2min de folga, pra cobrir jitter do scheduler) e notifica as
// que ainda não foram avisadas.
func (s *Server) pollSentryOnce(ctx context.Context, httpClient *http.Client, interval time.Duration) {
	windowMin := int(interval.Minutes()) + 2
	if windowMin < 3 {
		windowMin = 3
	}
	issues, err := s.fetchNewSentryIssues(ctx, httpClient, windowMin)
	if err != nil {
		slog.Warn("sentry poll: falha ao consultar issues", "err", err)
		return
	}
	for _, issue := range issues {
		s.notifySentryIssue(ctx, issue)
	}
}

func (s *Server) fetchNewSentryIssues(ctx context.Context, httpClient *http.Client, windowMin int) ([]sentryPollIssue, error) {
	q := url.Values{}
	for _, id := range sentryProjectIDs {
		q.Add("project", id)
	}
	q.Set("query", fmt.Sprintf("is:unresolved firstSeen:-%dm", windowMin))
	q.Set("sort", "new")
	q.Set("limit", "50")

	reqURL := "https://sentry.io/api/0/organizations/" + s.cfg.SentryOrgSlug + "/issues/?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.SentryAPIToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sentry respondeu %d", resp.StatusCode)
	}
	var issues []sentryPollIssue
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		return nil, err
	}
	return issues, nil
}

// notifySentryIssue envia o WhatsApp se: tenant_config.notif_enabled=true,
// há telefone/instância configurados, e a issue ainda não foi avisada
// (dedupe por ID no Redis, TTL de 7 dias — o suficiente pra não repetir
// enquanto ela seguir sem ação, sem silenciar pra sempre).
func (s *Server) notifySentryIssue(ctx context.Context, issue sentryPollIssue) {
	var phone, instance string
	var enabled bool
	err := s.pool.QueryRow(ctx,
		`SELECT notif_phone, notif_instance, notif_enabled FROM tenant_config WHERE tenant_id = $1`,
		s.cfg.TenantID,
	).Scan(&phone, &instance, &enabled)
	if err != nil || !enabled || phone == "" || instance == "" {
		return
	}
	if s.cfg.EvolutionAPIURL == "" || s.cfg.EvolutionAPIKey == "" {
		slog.Error("sentry notif: Evolution API não configurada")
		return
	}

	if !s.sentryNotifyAllow(ctx, issue.ID, 7*24*time.Hour) {
		return
	}

	text := buildSentryMessage(issue)
	evo := NewEvolutionClient(s.cfg.EvolutionAPIURL, s.cfg.EvolutionAPIKey, instance)
	if err := evo.SendText(ctx, phone, text); err != nil {
		slog.Error("sentry notif: falha ao enviar WhatsApp", "err", err, "phone", phone)
	}
}

func buildSentryMessage(issue sentryPollIssue) string {
	var b strings.Builder
	b.WriteString("🐛 Erro novo — ")
	b.WriteString(strings.ToUpper(issue.Level))
	b.WriteString("\nServiço: ")
	b.WriteString(issue.Project.Slug)
	b.WriteString("\n")
	b.WriteString(issue.Title)
	if issue.Culprit != "" {
		b.WriteString("\n")
		b.WriteString(issue.Culprit)
	}
	b.WriteString("\n")
	b.WriteString(issue.Permalink)
	return b.String()
}

// sentryNotifyAllow é o mesmo padrão de notifAllow (handlers_coolify.go),
// chave própria pra não colidir com o dedupe de deploy.
func (s *Server) sentryNotifyAllow(ctx context.Context, issueID string, window time.Duration) bool {
	key := fmt.Sprintf("bot:sentry-notif:%s:%s", s.cfg.TenantID, issueID)
	if s.rdb != nil {
		ok, err := s.rdb.SetNX(ctx, key, "1", window).Result()
		if err == nil {
			return ok
		}
		slog.Warn("sentry notif: dedupe via Redis falhou, usando fallback", "err", err)
	}
	if s.notifDedupe == nil {
		return true
	}
	return s.notifDedupe.allow(key, window)
}
