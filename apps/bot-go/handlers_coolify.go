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
	"time"
)

// handleCoolifyWebhook recebe eventos de deploy da Coolify e envia WhatsApp
// quando um deploy falha. URL: POST /webhooks/coolify?token=<COOLIFY_WEBHOOK_SECRET>
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

	if !coolifyIsFailedDeploy(payload) {
		w.WriteHeader(http.StatusOK)
		return
	}

	appName, _ := payload["application_name"].(string)
	if appName == "" {
		appName, _ = payload["name"].(string)
	}

	go s.sendCoolifyFailNotif(appName, payload)

	w.WriteHeader(http.StatusOK)
}

func coolifyIsFailedDeploy(p map[string]any) bool {
	status, _ := p["status"].(string)
	if strings.EqualFold(status, "failed") || strings.EqualFold(status, "error") {
		return true
	}
	eventType, _ := p["type"].(string)
	lower := strings.ToLower(eventType)
	if strings.Contains(lower, "fail") || strings.Contains(lower, "error") {
		return true
	}
	if dep, ok := p["deployment"].(map[string]any); ok {
		depStatus, _ := dep["status"].(string)
		if strings.EqualFold(depStatus, "failed") || strings.EqualFold(depStatus, "error") {
			return true
		}
	}
	return false
}

func (s *Server) sendCoolifyFailNotif(appName string, payload map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

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
		slog.Error("coolify notif: Evolution API não configurada")
		return
	}

	text := fmt.Sprintf("⚠️ Deploy falhou\nAplicação: %s", appName)
	if msg, _ := payload["message"].(string); msg != "" {
		text += "\n" + msg
	}

	evo := NewEvolutionClient(s.cfg.EvolutionAPIURL, s.cfg.EvolutionAPIKey, instance)
	if err := evo.SendText(ctx, phone, text); err != nil {
		slog.Error("coolify notif: falha ao enviar WhatsApp", "err", err, "phone", phone)
	}
}
