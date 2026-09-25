package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Retorno ao cliente — o "me chama dia X" que o bot gravava e nunca disparava.
//
// O modelo já devolvia `scheduledContact` e o engine gravava em
// scheduled_contacts com kind "reactivation". Mas o único leitor da fila
// (PendingFollowUps) só buscava kind "follow_up": o pedido ficava lá para
// sempre. Caso real que expôs isto (25/09/2026): uma cliente disse "em dezembro
// consigo mais" e ninguém seria avisado. O Henrique já fechou venda só porque
// anotou um retorno assim na agenda e recebeu o aviso no dia.
//
// Fase 1 da spec (dashboard/docs/superpowers/specs/2026-09-25-bot-follow-up-
// reativacao-design.md): na data, AVISA um humano — WhatsApp dos
// administradores e, com PLATFORM_API_TOKEN, uma Tarefa na plataforma para o
// responsável (a Tarefa já dispara sino + push). O bot não manda nada ao
// cliente nesta fase: reativar sozinho é a fase 2, com modo escolhido na tela.

// atrasoParaResumo — retorno vencido há mais que isto não vira aviso próprio.
// Ao entrar no ar, a fila tem pedidos parados há semanas; cada um virar uma
// mensagem seria uma rajada. Eles saem juntos, num resumo só.
const atrasoParaResumo = 7 * 24 * time.Hour

// confiancaDataFirme — abaixo disto a data veio de frase vaga ("em dezembro",
// "depois das férias") e o aviso diz que ela é aproximada.
const confiancaDataFirme = 0.6

type retornoPendente struct {
	ID        ScheduledContactID
	TenantID  TenantID
	ContactID ContactID
	ConvID    ConversationID
	FireAt    time.Time
	Frase     string
	Confianca float64
	Nome      string
	Telefone  string
	Canal     string
	Resumo    string
}

// payloadDoRetorno — o que fica gravado junto com o pedido. A frase original
// é o que dá contexto a quem vai retomar: "em dezembro consigo mais" diz muito
// mais que a data 01/12 sozinha.
func payloadDoRetorno(sc *ScheduledContact) []byte {
	raw, _ := json.Marshal(map[string]any{
		"kind":       "reactivation",
		"rawPhrase":  sc.RawPhrase,
		"confidence": sc.Confidence,
	})
	return raw
}

func (p retornoPendente) quem() string {
	switch {
	case p.Nome != "" && p.Telefone != "":
		return fmt.Sprintf("%s (%s)", p.Nome, p.Telefone)
	case p.Nome != "":
		return p.Nome
	default:
		return p.Telefone
	}
}

func linkDaConversa(painelBase string, conv ConversationID) string {
	return fmt.Sprintf("%s/admin/whats/conversas?c=%s", strings.TrimRight(painelBase, "/"), conv)
}

// avisoDeRetorno — a mensagem para o humano no dia do retorno.
func avisoDeRetorno(p retornoPendente, painelBase string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⏰ *Hoje é dia de retomar:* %s\n", p.quem())
	if p.Frase != "" {
		fmt.Fprintf(&b, "\nO que a pessoa disse: _\"%s\"_\n", p.Frase)
	}
	if p.Confianca > 0 && p.Confianca < confiancaDataFirme {
		b.WriteString("(data aproximada — a pessoa não cravou o dia)\n")
	}
	if p.Resumo != "" {
		fmt.Fprintf(&b, "\nResumo: %s\n", p.Resumo)
	}
	fmt.Fprintf(&b, "\nAbrir a conversa: %s", linkDaConversa(painelBase, p.ConvID))
	return b.String()
}

// separaAtrasados divide a fila entre o que vence agora e o que ficou parado.
func separaAtrasados(ps []retornoPendente, agora time.Time) (noPrazo, atrasados []retornoPendente) {
	for _, p := range ps {
		if agora.Sub(p.FireAt) > atrasoParaResumo {
			atrasados = append(atrasados, p)
		} else {
			noPrazo = append(noPrazo, p)
		}
	}
	return noPrazo, atrasados
}

// resumoDeAtrasados — uma mensagem só com todos os retornos que o sistema
// deixou de avisar.
func resumoDeAtrasados(at []retornoPendente, painelBase string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📋 *%d retornos atrasados* — clientes pediram para ser chamados e o sistema não avisou na época:\n", len(at))
	for _, p := range at {
		fmt.Fprintf(&b, "\n• %s — pediu para %s", p.quem(), p.FireAt.Format("02/01"))
		if p.Frase != "" {
			fmt.Fprintf(&b, ": _\"%s\"_", p.Frase)
		}
		fmt.Fprintf(&b, "\n  %s", linkDaConversa(painelBase, p.ConvID))
	}
	return b.String()
}

// PendingReactivations — retornos vencidos, com o que o aviso precisa (nome,
// telefone, canal, resumo do lead) numa consulta só.
func (r *ScheduledContactRepo) PendingReactivations(ctx context.Context, limit int) ([]retornoPendente, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT sc.id, sc.tenant_id, sc.contact_id, sc.conversation_id, sc.fire_at,
		       COALESCE(sc.payload->>'rawPhrase', ''),
		       COALESCE((sc.payload->>'confidence')::float8, 0),
		       COALESCE(c.display_name, ''),
		       COALESCE(ci.external_id, ''),
		       COALESCE(ci.channel::text, ''),
		       COALESCE(ls.summary, '')
		FROM scheduled_contacts sc
		LEFT JOIN contact c ON c.id = sc.contact_id
		LEFT JOIN LATERAL (
			SELECT external_id, channel FROM channel_identity
			WHERE tenant_id = sc.tenant_id AND contact_id = sc.contact_id
			ORDER BY created_at ASC LIMIT 1
		) ci ON true
		LEFT JOIN lead_summary ls ON ls.tenant_id = sc.tenant_id AND ls.contact_id = sc.contact_id
		WHERE sc.payload->>'kind' = 'reactivation'
		  AND sc.status = 'pending'
		  AND sc.fire_at <= now()
		ORDER BY sc.fire_at
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("ScheduledContactRepo.PendingReactivations: %w", err)
	}
	defer rows.Close()
	var out []retornoPendente
	for rows.Next() {
		var p retornoPendente
		if err := rows.Scan(&p.ID, &p.TenantID, &p.ContactID, &p.ConvID, &p.FireAt,
			&p.Frase, &p.Confianca, &p.Nome, &p.Telefone, &p.Canal, &p.Resumo); err != nil {
			return nil, fmt.Errorf("ScheduledContactRepo.PendingReactivations: scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// runReactivations avisa os retornos vencidos. Marca como disparado assim que
// o aviso sai (sucesso parcial conta: um admin avisado já basta), para o mesmo
// retorno não ser avisado de novo no próximo ciclo.
func (w *Worker) runReactivations(ctx context.Context) {
	ps, err := w.deps.Scheduled.PendingReactivations(ctx, 100)
	if err != nil {
		if ctx.Err() == nil {
			w.deps.Logger.Error("runReactivations: erro ao buscar retornos", "err", err)
		}
		return
	}
	if len(ps) == 0 {
		return
	}
	painel := w.deps.Config.SiteURL + "/dashboard"
	noPrazo, atrasados := separaAtrasados(ps, time.Now())

	for _, p := range noPrazo {
		if err := w.avisaRetorno(ctx, p.TenantID, p.Canal, avisoDeRetorno(p, painel)); err != nil {
			w.deps.Logger.Error("runReactivations: aviso falhou", "id", p.ID, "err", err)
			_ = w.deps.Scheduled.MarkFollowUpFailed(ctx, p.ID, err.Error())
			continue
		}
		w.criaTarefaDeRetorno(ctx, p, painel)
		if err := w.deps.Scheduled.MarkFollowUpSent(ctx, p.ID); err != nil {
			w.deps.Logger.Error("runReactivations: erro ao marcar disparado", "id", p.ID, "err", err)
		}
	}

	if len(atrasados) > 0 {
		if err := w.avisaRetorno(ctx, atrasados[0].TenantID, "", resumoDeAtrasados(atrasados, painel)); err != nil {
			w.deps.Logger.Error("runReactivations: resumo de atrasados falhou", "n", len(atrasados), "err", err)
			return
		}
		for _, p := range atrasados {
			w.criaTarefaDeRetorno(ctx, p, painel)
			if err := w.deps.Scheduled.MarkFollowUpSent(ctx, p.ID); err != nil {
				w.deps.Logger.Error("runReactivations: erro ao marcar atrasado", "id", p.ID, "err", err)
			}
		}
	}
	w.deps.Logger.Info("runReactivations: retornos avisados", "noPrazo", len(noPrazo), "atrasados", len(atrasados))
}

func (w *Worker) avisaRetorno(ctx context.Context, tenant TenantID, canal, texto string) error {
	return w.notificaAdmin(ctx, DomainEvent{
		Type:     "reactivation.due",
		TenantID: tenant,
		Payload:  map[string]any{"type": "REACTIVATION", "question": texto, "channel": canal},
	})
}

// criaTarefaDeRetorno lança a Tarefa na plataforma para o responsável. É um
// canal a mais, não o principal: sem token ou com a API fora, o aviso de
// WhatsApp já saiu e o retorno segue marcado — nunca trava a fila.
func (w *Worker) criaTarefaDeRetorno(ctx context.Context, p retornoPendente, painel string) {
	cfg := w.deps.Config
	if cfg.PlatformAPIToken == "" || cfg.FollowUpResponsavelID <= 0 {
		return
	}
	desc := avisoDeRetorno(p, painel)
	body, _ := json.Marshal(map[string]any{
		"title":         "Retomar " + p.quem(),
		"status":        "a_fazer",
		"priority":      "alta",
		"description":   desc,
		"responsavelId": cfg.FollowUpResponsavelID,
		"dueDate":       p.FireAt.UTC().Format(time.RFC3339),
	})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// AGENT_GO_URL já é o host da API central (api.santos-tech.com), onde vivem as Tarefas.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.AgentGoURL+"/tasks", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+cfg.PlatformAPIToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.deps.Logger.Warn("criaTarefaDeRetorno: API indisponível", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		w.deps.Logger.Warn("criaTarefaDeRetorno: tarefa não criada", "status", resp.StatusCode)
	}
}
