package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// Workers agrupa as dependências dos handlers de job.
type Workers struct {
	cfg    Config
	store  *Store
	evo    *EvolutionClient
	cool   *CoolifyClient
	client *asynq.Client
}

// react reage na mensagem-âncora, degradando com graça (a reação é cosmética).
func (w *Workers) react(ctx context.Context, anchor, emoji string) {
	if anchor == "" {
		return
	}
	if err := w.evo.SendReaction(ctx, w.cfg.GroupJID, anchor, emoji); err != nil {
		slog.Warn("falha ao reagir (segue)", "err", err, "emoji", emoji)
	}
}

// HandleIncidentCreate posta o log-âncora no grupo, reage 🔧, grava o incidente e
// enfileira o fix.
func (w *Workers) HandleIncidentCreate(ctx context.Context, t *asynq.Task) error {
	var p IncidentPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return err
	}

	text := fmt.Sprintf("🔧 Build de *%s* falhou (commit %s). Vou tentar consertar…",
		p.App, shortSHA(p.Commit))
	anchor, err := w.evo.SendText(ctx, w.cfg.GroupJID, text, "")
	if err != nil {
		// Sem âncora não há thread de status — ainda assim seguimos com o fix.
		slog.Error("falha ao postar log-âncora (segue)", "err", err)
	}
	w.react(ctx, anchor, "🔧")

	in := &Incident{
		ID: uuid.NewString(), App: p.App, AppUUID: p.AppUUID, Repo: p.Repo,
		Branch: p.Branch, Commit: p.Commit, AnchorMsgID: anchor,
		Status: "fixing", Attempts: 1, DeploymentUUID: p.DeploymentUUID,
	}
	if err := w.store.Save(ctx, in); err != nil {
		return err
	}
	if err := w.store.SetActive(ctx, in); err != nil {
		return err
	}
	_, err = w.client.Enqueue(newTask(TaskFixRun, IDPayload{IncidentID: in.ID}))
	return err
}

// HandleFixRun clona o repo, roda o Claude, e (se mudou algo) commita e dá push,
// deixando o incidente aguardando o rebuild.
func (w *Workers) HandleFixRun(ctx context.Context, t *asynq.Task) error {
	var p IDPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return err
	}
	in, err := w.store.Load(ctx, p.IncidentID)
	if err != nil {
		return err
	}
	if in == nil || in.Status == "done" {
		return nil
	}

	// Resolve o repositório/branch se o webhook não trouxe.
	if (in.Repo == "" || in.Branch == "") && w.cool != nil && in.AppUUID != "" {
		if repo, branch, e := w.cool.AppRepo(ctx, in.AppUUID); e == nil {
			if in.Repo == "" {
				in.Repo = repo
			}
			if in.Branch == "" {
				in.Branch = branch
			}
		}
	}
	if in.Repo == "" || in.Branch == "" {
		return w.fail(ctx, in, "não consegui resolver o repositório/branch do app")
	}

	workdir := workdirFor(w.cfg.WorkspaceRoot, in.App, in.ID)
	if err := cloneRepo(ctx, in.Repo, in.Branch, workdir, w.cfg.GithubToken); err != nil {
		return w.fail(ctx, in, "falha ao clonar o repo: "+err.Error())
	}

	var buildLogs string
	if w.cool != nil && in.DeploymentUUID != "" {
		buildLogs, _ = w.cool.BuildLogs(ctx, in.DeploymentUUID)
	}

	summary, err := runClaudeFix(ctx, w.cfg, workdir, buildLogs, in.Commit)
	if err != nil && summary == "" {
		return w.fail(ctx, in, "o Claude não conseguiu rodar: "+err.Error())
	}

	commitMsg := "fix(auto): " + firstLine(summary) + " [skip auto-fix]"
	changed, err := commitAll(ctx, workdir, commitMsg)
	if err != nil {
		return w.fail(ctx, in, "falha no commit: "+err.Error())
	}
	if !changed {
		return w.fail(ctx, in, "o Claude não alterou nada — sem fix para aplicar")
	}

	if err := pushBranch(ctx, workdir, in.Branch, w.cfg.GithubToken); err != nil {
		return w.fail(ctx, in, "falha no push: "+err.Error())
	}

	sha, _ := headSHA(ctx, workdir)
	in.FixSHA = sha
	in.LastSummary = summary
	in.Status = "awaiting_rebuild"
	if err := w.store.Save(ctx, in); err != nil {
		return err
	}
	if sha != "" {
		_ = w.store.BindSHA(ctx, sha, in.ID)
	}

	// Agenda o timeout do rebuild (cobre o caso do 2º webhook nunca chegar).
	_, err = w.client.Enqueue(
		newTask(TaskDeployTimeout, IDPayload{IncidentID: in.ID}),
		asynq.ProcessIn(time.Duration(w.cfg.RebuildTimeout)*time.Minute),
	)
	return err
}

// HandleNotifyFinal fecha o incidente: troca a reação e dá reply na âncora.
func (w *Workers) HandleNotifyFinal(ctx context.Context, t *asynq.Task) error {
	var p NotifyPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return err
	}
	in, err := w.store.Load(ctx, p.IncidentID)
	if err != nil {
		return err
	}
	if in == nil || in.Status == "done" || in.Status == "failed" {
		return nil
	}

	if p.Outcome == "success" {
		w.react(ctx, in.AnchorMsgID, "✅")
		msg := fmt.Sprintf("✅ Corrigido em %s: %s", shortSHA(in.FixSHA), firstLine(in.LastSummary))
		_, _ = w.evo.SendText(ctx, w.cfg.GroupJID, msg, in.AnchorMsgID)
		in.Status = "done"
	} else {
		w.react(ctx, in.AnchorMsgID, "💀")
		reason := p.Reason
		if reason == "" {
			reason = "não foi possível corrigir automaticamente"
		}
		msg := fmt.Sprintf("💀 Não consegui corrigir automaticamente (%s). Precisa de um humano.", reason)
		_, _ = w.evo.SendText(ctx, w.cfg.GroupJID, msg, in.AnchorMsgID)
		in.Status = "failed"
	}
	_ = w.store.Save(ctx, in)
	return w.store.ClearActive(ctx, in.App)
}

// HandleDeployTimeout dispara se o rebuild não confirmou dentro da janela.
func (w *Workers) HandleDeployTimeout(ctx context.Context, t *asynq.Task) error {
	var p IDPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return err
	}
	in, err := w.store.Load(ctx, p.IncidentID)
	if err != nil {
		return err
	}
	if in == nil || in.Status != "awaiting_rebuild" {
		return nil // já resolveu
	}
	w.react(ctx, in.AnchorMsgID, "⏰")
	msg := fmt.Sprintf("⏰ Empurrei o fix %s mas não recebi confirmação do rebuild em %dmin. Confere manualmente.",
		shortSHA(in.FixSHA), w.cfg.RebuildTimeout)
	_, _ = w.evo.SendText(ctx, w.cfg.GroupJID, msg, in.AnchorMsgID)
	in.Status = "failed"
	_ = w.store.Save(ctx, in)
	return w.store.ClearActive(ctx, in.App)
}

// fail marca o incidente como falho e notifica (usado nos abortos do fix).
func (w *Workers) fail(ctx context.Context, in *Incident, reason string) error {
	w.react(ctx, in.AnchorMsgID, "💀")
	msg := fmt.Sprintf("💀 Não consegui corrigir automaticamente (%s). Precisa de um humano.", reason)
	_, _ = w.evo.SendText(ctx, w.cfg.GroupJID, msg, in.AnchorMsgID)
	in.Status = "failed"
	_ = w.store.Save(ctx, in)
	return w.store.ClearActive(ctx, in.App)
}
