package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
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
	ghApp  *GitHubApp // pode ser nil/disabled → cai pro PAT
	client *asynq.Client
}

// repoToken devolve o token de git para o incidente: preferencialmente um
// installation token efêmero escopado ao repo (GitHub App); cai para o PAT
// (GITHUB_TOKEN) com WARN de downgrade quando o App não está disponível.
func (w *Workers) repoToken(ctx context.Context, repoURL string) string {
	if w.ghApp.Enabled() {
		if owner, repo, err := parseRepoURL(repoURL); err == nil {
			if tok, err := w.ghApp.RepoToken(ctx, time.Now(), owner, repo); err == nil {
				return tok
			} else {
				slog.Warn("github app falhou — usando PAT (downgrade de postura)", "err", err, "repo", repoURL)
			}
		} else {
			slog.Warn("não consegui parsear o repo p/ o github app — usando PAT", "err", err, "repo", repoURL)
		}
	}
	return w.cfg.GithubToken
}

// evoTimeout limita cada chamada à Evolution (rede externa) para não pendurar o job.
const evoTimeout = 10 * time.Second

// sendText posta no grupo com timeout próprio (a chamada externa não deve pendurar
// o job mesmo se o ctx do worker for longo).
func (w *Workers) sendText(ctx context.Context, text, quoted string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, evoTimeout)
	defer cancel()
	return w.evo.SendText(ctx, w.cfg.GroupJID, text, quoted)
}

// react reage na mensagem-âncora, degradando com graça (a reação é cosmética).
func (w *Workers) react(ctx context.Context, anchor, emoji string) {
	if anchor == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, evoTimeout)
	defer cancel()
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
	anchor, err := w.sendText(ctx, text, "")
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

	// Allow-list (#5): o repo vem do payload do webhook / da API da Coolify. Sem
	// esta checagem, quem controlasse esse valor fazia o auto-fixer clonar
	// qualquer repositório usando o token da organização.
	if !w.cfg.repoAllowed(in.Repo) {
		return w.fail(ctx, in, "repositório fora da allow-list (ALLOWED_REPOS): "+in.Repo)
	}

	token := w.repoToken(ctx, in.Repo)
	cloneURL := repoCloneURL(in.Repo) // Coolify devolve "owner/repo" → URL https
	workdir, err := workdirFor(w.cfg.WorkspaceRoot, in.App, in.ID)
	if err != nil {
		return w.fail(ctx, in, "workdir inválido: "+err.Error())
	}
	// (#10) O clone ficava no volume para sempre — um repo por incidente até o
	// disco encher. Some ao fim do run, em qualquer caminho de saída.
	defer func() {
		if rerr := os.RemoveAll(workdir); rerr != nil {
			slog.Warn("não consegui limpar o workdir do incidente", "err", rerr, "workdir", workdir)
		}
	}()
	if err := cloneRepo(ctx, cloneURL, in.Branch, workdir, token); err != nil {
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

	if err := pushBranch(ctx, workdir, in.Branch, token); err != nil {
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
		_, _ = w.sendText(ctx, msg, in.AnchorMsgID)
		in.Status = "done"
	} else {
		w.react(ctx, in.AnchorMsgID, "💀")
		reason := p.Reason
		if reason == "" {
			reason = "não foi possível corrigir automaticamente"
		}
		msg := fmt.Sprintf("💀 Não consegui corrigir automaticamente (%s). Precisa de um humano.", reason)
		_, _ = w.sendText(ctx, msg, in.AnchorMsgID)
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
	_, _ = w.sendText(ctx, msg, in.AnchorMsgID)
	in.Status = "failed"
	_ = w.store.Save(ctx, in)
	return w.store.ClearActive(ctx, in.App)
}

// fail marca o incidente como falho e notifica (usado nos abortos do fix).
func (w *Workers) fail(ctx context.Context, in *Incident, reason string) error {
	w.react(ctx, in.AnchorMsgID, "💀")
	msg := fmt.Sprintf("💀 Não consegui corrigir automaticamente (%s). Precisa de um humano.", reason)
	_, _ = w.sendText(ctx, msg, in.AnchorMsgID)
	in.Status = "failed"
	_ = w.store.Save(ctx, in)
	return w.store.ClearActive(ctx, in.App)
}
