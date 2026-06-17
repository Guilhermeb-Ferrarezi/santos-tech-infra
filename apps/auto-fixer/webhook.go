package main

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hibiken/asynq"
)

// WebhookHandler recebe os eventos de deploy da Coolify e orquestra o ciclo de
// auto-fix enfileirando jobs. Responde 200 sempre (a Coolify não deve reenfileirar
// por erro nosso).
type WebhookHandler struct {
	cfg    Config
	client *asynq.Client
	store  *Store
	cool   *CoolifyClient // pode ser nil (degrada: sem enriquecimento de commit/logs)
}

func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.cfg.WebhookSecret != "" {
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(h.cfg.WebhookSecret)) != 1 {
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

	switch coolifyDeployStatus(payload) {
	case "failed":
		h.handleFailure(r, payload)
	case "success":
		h.handleSuccess(r, payload)
	}
	w.WriteHeader(http.StatusOK)
}

// handleFailure decide entre: (a) rebuild do nosso próprio fix que falhou
// (loop-guard → retry ou desiste), ou (b) falha nova → abre incidente.
func (h *WebhookHandler) handleFailure(r *http.Request, payload map[string]any) {
	ctx := r.Context()
	app := coolifyAppName(payload)
	du := coolifyString(payload, "deployment_uuid", "deployment_id", "deployment")
	branch := coolifyString(payload, "git_branch", "branch", "ref")

	// Webhook duplicado (mesmo deployment) → ignora.
	if du != "" {
		first, err := h.store.DedupeDeployment(ctx, du, 5*time.Minute)
		if err == nil && !first {
			return
		}
	}

	// O webhook não traz commit/mensagem; busca via API quando possível.
	var commit, cmsg string
	if h.cool != nil && du != "" {
		commit, cmsg, _ = h.cool.DeploymentInfo(ctx, du)
	}

	// Loop-guard: é o rebuild do nosso próprio fix que voltou a falhar?
	active, _ := h.store.ActiveByApp(ctx, app)
	if active != nil && active.Status == "awaiting_rebuild" &&
		((commit != "" && commit == active.FixSHA) || strings.Contains(cmsg, "[skip auto-fix]")) {
		if active.Attempts < h.cfg.MaxFixAttempts {
			active.Attempts++
			active.Status = "fixing"
			_ = h.store.Save(ctx, active)
			h.enqueue(TaskFixRun, IDPayload{IncidentID: active.ID})
			slog.Info("loop-guard: nova tentativa de fix", "app", app, "attempt", active.Attempts)
		} else {
			h.enqueue(TaskNotifyFinal, NotifyPayload{IncidentID: active.ID, Outcome: "failed", Reason: "máx de tentativas atingido"})
			slog.Warn("loop-guard: desistindo", "app", app, "attempts", active.Attempts)
		}
		return
	}

	// Falha de um commit nosso (já tratado) que não casou acima → não re-dispara.
	if strings.Contains(cmsg, "[skip auto-fix]") {
		return
	}

	h.enqueue(TaskIncidentCreate, IncidentPayload{
		App: app, DeploymentUUID: du, Branch: branch, Commit: commit, CommitMsg: cmsg,
	})
}

// handleSuccess fecha o incidente quando o rebuild do nosso fix passou.
func (h *WebhookHandler) handleSuccess(r *http.Request, payload map[string]any) {
	ctx := r.Context()
	app := coolifyAppName(payload)
	du := coolifyString(payload, "deployment_uuid", "deployment_id", "deployment")

	var commit string
	if h.cool != nil && du != "" {
		commit, _, _ = h.cool.DeploymentInfo(ctx, du)
	}

	// Correlaciona pelo SHA do fix; se não der, cai pro incidente ativo do app.
	var inc *Incident
	if commit != "" {
		inc, _ = h.store.BySHA(ctx, commit)
	}
	if inc == nil {
		inc, _ = h.store.ActiveByApp(ctx, app)
	}
	if inc != nil && inc.Status == "awaiting_rebuild" {
		h.enqueue(TaskNotifyFinal, NotifyPayload{IncidentID: inc.ID, Outcome: "success"})
	}
}

func (h *WebhookHandler) enqueue(name string, v any) {
	if _, err := h.client.Enqueue(newTask(name, v)); err != nil {
		slog.Error("falha ao enfileirar", "task", name, "err", err)
	}
}
