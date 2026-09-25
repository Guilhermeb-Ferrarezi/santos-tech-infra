package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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

	// Fase 2 (reativacao_modos.go): o tipo do retorno e o estado da conversa,
	// para decidir se o bot pode mandar sozinho.
	Kind            string // KindRetorno | KindPosExperimental
	CriadoEm        time.Time
	AulaEm          *time.Time // só no pós-experimental
	BotLigado       bool
	Estado          string
	UltimaDoCliente *time.Time
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
	if p.Kind == KindPosExperimental && p.AulaEm != nil {
		fmt.Fprintf(&b, "\nFez a aula experimental em %s e ainda não fechou.\n", p.AulaEm.In(brLocation).Format("02/01"))
	}
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
		       COALESCE(cv.channel::text, ci.channel::text, ''),
		       COALESCE(ls.summary, ''),
		       sc.payload->>'kind',
		       sc.created_at,
		       (sc.payload->>'aulaEm')::timestamptz,
		       COALESCE(cv.bot_enabled, false),
		       COALESCE(cv.state::text, ''),
		       cv.last_inbound_at
		FROM scheduled_contacts sc
		LEFT JOIN contact c ON c.id = sc.contact_id
		LEFT JOIN conversation cv ON cv.tenant_id = sc.tenant_id AND cv.id = sc.conversation_id
		-- O telefone da PRÓPRIA conversa; sem conversa, o primeiro do contato.
		LEFT JOIN LATERAL (
			SELECT external_id, channel FROM channel_identity
			WHERE tenant_id = sc.tenant_id AND contact_id = sc.contact_id
			ORDER BY (id = cv.channel_identity_id) DESC, created_at ASC LIMIT 1
		) ci ON true
		LEFT JOIN lead_summary ls ON ls.tenant_id = sc.tenant_id AND ls.contact_id = sc.contact_id
		WHERE sc.payload->>'kind' IN ('reactivation', 'pos_experimental')
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
			&p.Frase, &p.Confianca, &p.Nome, &p.Telefone, &p.Canal, &p.Resumo,
			&p.Kind, &p.CriadoEm, &p.AulaEm, &p.BotLigado, &p.Estado, &p.UltimaDoCliente); err != nil {
			return nil, fmt.Errorf("ScheduledContactRepo.PendingReactivations: scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// runReactivations dispara os retornos vencidos no modo escolhido na tela
// (reativacao_modos.go). Marca como disparado assim que o aviso ou a mensagem
// sai (sucesso parcial conta: um admin avisado já basta), para o mesmo retorno
// não sair de novo no próximo ciclo.
func (w *Worker) runReactivations(ctx context.Context) {
	w.enfileiraPosExperimental(ctx)

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

	cfgs := map[TenantID]*TenantConfig{}
	for _, p := range noPrazo {
		cfg := w.configDoTenant(ctx, cfgs, p.TenantID)
		acao := decideAcao(cfg.FollowupModo, p, condicoesDoRetorno(p, time.Now(), cfg.EvolutionBotReplyEnabled))

		enviado := ""
		if acao.Reativar {
			texto, err := w.reativa(ctx, p, *cfg)
			if err != nil {
				// A mensagem não saiu: o retorno não se perde, vira aviso.
				w.deps.Logger.Warn("runReactivations: reativação não saiu, avisando", "id", p.ID, "err", err)
				acao = acaoRetorno{Avisar: true, Motivo: "a mensagem automática não saiu (detalhe no log do bot)"}
			} else {
				enviado = texto
			}
		}

		if acao.Avisar {
			if err := w.avisaRetorno(ctx, p.TenantID, p.Canal, avisoComAcao(p, painel, acao, enviado)); err != nil {
				w.deps.Logger.Error("runReactivations: aviso falhou", "id", p.ID, "err", err)
				if enviado == "" {
					_ = w.deps.Scheduled.MarkFollowUpFailed(ctx, p.ID, err.Error())
					continue
				}
			}
			w.criaTarefaDeRetorno(ctx, p, painel, cfg.FollowupResponsavelID)
		}
		if err := w.deps.Scheduled.MarkFollowUpSent(ctx, p.ID); err != nil {
			w.deps.Logger.Error("runReactivations: erro ao marcar disparado", "id", p.ID, "err", err)
		}
	}

	// Atrasados nunca são reativados sozinhos: mensagem automática sobre um
	// pedido de semanas atrás é exatamente o que queima o lead.
	if len(atrasados) > 0 {
		if err := w.avisaRetorno(ctx, atrasados[0].TenantID, "", resumoDeAtrasados(atrasados, painel)); err != nil {
			w.deps.Logger.Error("runReactivations: resumo de atrasados falhou", "n", len(atrasados), "err", err)
			return
		}
		cfg := w.configDoTenant(ctx, cfgs, atrasados[0].TenantID)
		for _, p := range atrasados {
			w.criaTarefaDeRetorno(ctx, p, painel, cfg.FollowupResponsavelID)
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
//
// responsavelDaTela é o escolhido em WhatsApp · Configurações (0 = o do
// ambiente, FOLLOWUP_RESPONSAVEL_ID).
func (w *Worker) criaTarefaDeRetorno(ctx context.Context, p retornoPendente, painel string, responsavelDaTela int) {
	cfg := w.deps.Config
	responsavel := responsavelEfetivo(responsavelDaTela, cfg.FollowUpResponsavelID)
	if cfg.PlatformAPIToken == "" || responsavel <= 0 {
		return
	}
	desc := avisoDeRetorno(p, painel)
	titulo := "Retomar " + p.quem()
	if p.Kind == KindPosExperimental {
		titulo += " (pós-experimental)"
	}
	body, _ := json.Marshal(map[string]any{
		"title":         titulo,
		"status":        "a_fazer",
		"priority":      "alta",
		"description":   desc,
		"responsavelId": responsavel,
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

// configDoTenant lê a configuração uma vez por ciclo e tenant. Sem ela, vale o
// padrão seguro: só avisar, responsável do ambiente, pós-experimental desligado.
func (w *Worker) configDoTenant(ctx context.Context, cache map[TenantID]*TenantConfig, tenant TenantID) *TenantConfig {
	if cfg, ok := cache[tenant]; ok {
		return cfg
	}
	cfg := &TenantConfig{TenantID: tenant, FollowupModo: ModoAvisar}
	if w.deps.TenantCfg != nil {
		if c, err := w.deps.TenantCfg.Get(ctx, nil, tenant); err == nil {
			cfg = c
		} else if ctx.Err() == nil {
			w.deps.Logger.Warn("retorno: config do tenant indisponível, só avisando", "tenant", tenant, "err", err)
		}
	}
	cache[tenant] = cfg
	return cfg
}

// reativa manda ao cliente a mensagem de retorno e grava no histórico, para a
// conversa continuar com contexto quando a pessoa responder. Devolve o texto
// enviado; erro = nada saiu, e o chamador cai para "avisar".
func (w *Worker) reativa(ctx context.Context, p retornoPendente, cfg TenantConfig) (string, error) {
	if p.ConvID == "" || p.Telefone == "" {
		return "", fmt.Errorf("retorno sem conversa ou telefone")
	}
	if w.deps.AgentGo == nil {
		return "", fmt.Errorf("modelo indisponível")
	}
	sender := w.deps.Sender
	if p.Canal == "evolution" {
		sender = w.deps.EvolutionSender
	}
	if sender == nil {
		return "", fmt.Errorf("sem envio configurado para o canal %q", p.Canal)
	}

	bruto, err := w.deps.AgentGo.RespondWithModel(ctx, promptDeReativacao(p, cfg), "haiku", false)
	if err != nil {
		return "", fmt.Errorf("modelo: %w", err)
	}
	texto := limpaMensagemDoModelo(bruto)
	if texto == "" {
		return "", fmt.Errorf("o modelo não devolveu uma mensagem usável")
	}

	idem := "reativacao-" + string(p.ID)
	provID, err := sender.SendMessage(ctx, OutboundMessage{
		TenantID:       p.TenantID,
		ConversationID: p.ConvID,
		Channel:        p.Canal,
		To:             p.Telefone,
		Intent:         IntentFreeForm,
		Content:        MessageContent{Type: "text", Text: texto},
		IdempotencyKey: idem,
	})
	if err != nil {
		return "", fmt.Errorf("envio: %w", err)
	}

	// A mensagem já saiu: falha ao gravar no histórico não desfaz o envio, só
	// fica registrada.
	if w.deps.Pool != nil && w.deps.Messages != nil {
		if err := withTenant(w.deps.Pool, p.TenantID)(ctx, func(tx pgx.Tx) error {
			return w.deps.Messages.RecordOutbound(ctx, tx, idem, p.TenantID, p.ConvID, provID, texto, nil)
		}); err != nil {
			w.deps.Logger.Error("reativa: enviada mas não gravada no histórico", "id", p.ID, "err", err)
		}
	}
	w.deps.Logger.Info("reativa: cliente reativado", "id", p.ID, "kind", p.Kind, "conv", p.ConvID)
	return texto, nil
}

// enfileiraPosExperimental põe na fila de retornos as aulas experimentais cujo
// prazo (data da aula + N dias, 9h de Brasília) venceu. Dias = 0 desliga.
func (w *Worker) enfileiraPosExperimental(ctx context.Context) {
	if w.deps.Scheduled == nil || w.deps.Config.TenantID == "" {
		return
	}
	tenant := TenantID(w.deps.Config.TenantID)
	cfg := w.configDoTenant(ctx, map[TenantID]*TenantConfig{}, tenant)
	if cfg.FollowupDiasPosExperimental <= 0 {
		return
	}
	n, err := w.deps.Scheduled.EnfileiraPosExperimental(ctx, tenant, cfg.FollowupDiasPosExperimental)
	if err != nil {
		if ctx.Err() == nil {
			w.deps.Logger.Error("enfileiraPosExperimental: falhou", "err", err)
		}
		return
	}
	if n > 0 {
		w.deps.Logger.Info("enfileiraPosExperimental: aulas na fila de retorno", "n", n, "dias", cfg.FollowupDiasPosExperimental)
	}
}

// janelaPosExperimental — até quanto tempo depois do prazo uma aula ainda entra
// na fila. Ao ligar a função (ou depois de o worker ficar parado), aulas antigas
// não viram uma rajada de retornos sobre aulas de semanas atrás.
const janelaPosExperimental = 3 * 24 * time.Hour

// EnfileiraPosExperimental grava um retorno kind "pos_experimental" para cada
// aula do bot (booking_reminder) que:
//   - teve o prazo vencido há menos de janelaPosExperimental;
//   - não tem resultado marcado, ou tem "veio" (ainda decidindo) — "fechou",
//     "nao_fechou" e "faltou" não recebem este retorno;
//   - tem conversa conhecida;
//   - não tem um retorno pedido pelo próprio cliente ainda por vir ("me chama
//     em dezembro" vence o "e aí, o que achou da aula?").
//
// O índice único da migration 0042 garante um retorno por aula, para sempre.
func (r *ScheduledContactRepo) EnfileiraPosExperimental(ctx context.Context, tenant TenantID, dias int) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		WITH aulas AS (
			SELECT DISTINCT ON (br.notion_page_id)
			       br.notion_page_id, br.client_phone, br.channel, br.aluno, br.aula_em,
			       br.conversation_id
			FROM booking_reminder br
			WHERE br.tenant_id = $1
			  AND br.status <> 'cancelado'
			  AND br.aula_em >= now() - make_interval(days => $2 + 4)
			ORDER BY br.notion_page_id, br.aula_em
		), devidas AS (
			SELECT a.*,
			       (((a.aula_em AT TIME ZONE 'America/Sao_Paulo')::date + $2) + time '09:00')
			         AT TIME ZONE 'America/Sao_Paulo' AS vence_em
			FROM aulas a
		)
		INSERT INTO scheduled_contacts (tenant_id, contact_id, conversation_id, fire_at, status, payload)
		SELECT $1, ci.contact_id, COALESCE(d.conversation_id, cv.id), d.vence_em, 'pending',
		       jsonb_build_object('kind', 'pos_experimental', 'notionPageId', d.notion_page_id,
		                          'aluno', d.aluno, 'aulaEm', d.aula_em)
		FROM devidas d
		JOIN LATERAL (
			SELECT id, contact_id FROM channel_identity
			WHERE tenant_id = $1 AND external_id = d.client_phone
			ORDER BY (channel::text = d.channel) DESC, created_at ASC LIMIT 1
		) ci ON true
		LEFT JOIN LATERAL (
			SELECT id FROM conversation
			WHERE tenant_id = $1 AND channel_identity_id = ci.id
			ORDER BY updated_at DESC LIMIT 1
		) cv ON true
		LEFT JOIN aula_resultado ar ON ar.tenant_id = $1 AND ar.notion_page_id = d.notion_page_id
		WHERE d.vence_em <= now()
		  AND d.vence_em > now() - $3::interval
		  AND COALESCE(ar.resultado, 'veio') = 'veio'
		  AND COALESCE(d.conversation_id, cv.id) IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1 FROM scheduled_contacts pedido
			WHERE pedido.tenant_id = $1 AND pedido.contact_id = ci.contact_id
			  AND pedido.payload->>'kind' = 'reactivation'
			  AND pedido.status = 'pending' AND pedido.fire_at > now()
		  )
		ON CONFLICT DO NOTHING
	`, tenant, dias, fmt.Sprintf("%d hours", int(janelaPosExperimental.Hours())))
	if err != nil {
		return 0, fmt.Errorf("ScheduledContactRepo.EnfileiraPosExperimental: %w", err)
	}
	return tag.RowsAffected(), nil
}
