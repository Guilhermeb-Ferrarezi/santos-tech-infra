package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Interfaces
// ---------------------------------------------------------------------------

// Responder invoca o LLM para produzir balões de resposta.
type Responder interface {
	Respond(ctx context.Context, conv Conversation, convCtx ConversationContext, cfg TenantConfig, inboundText string) (ResponderOutput, error)
}

// ChatSender envia mensagens de saída pelo canal (WhatsApp, etc.).
type ChatSender interface {
	SendMessage(ctx context.Context, msg OutboundMessage) (providerMessageID string, err error)
	SendText(ctx context.Context, to, text string) error
}

// EventEmitter grava domain events no outbox (dentro da transação corrente).
type EventEmitter interface {
	Emit(ctx context.Context, tx pgx.Tx, event DomainEvent) error
}

// ---------------------------------------------------------------------------
// EngineDeps
// ---------------------------------------------------------------------------

// EngineDeps agrupa todas as dependências injetadas no ConversationEngine.
type EngineDeps struct {
	TenantID  TenantID
	DB        *pgxpool.Pool
	Contacts  *ContactRepo
	Convs     *ConversationRepo
	Messages  *MessageRepo
	Leads     *LeadRepo
	Config    *TenantConfigRepo
	Responder Responder
	Sender    ChatSender
	Emitter   EventEmitter
	Logger    *slog.Logger
	// Broadcast envia um evento WebSocket a todos os clientes do dashboard (opcional).
	Broadcast func(ev WSEvent)
	// LogRepo persiste logs de processamento para o painel de logs (opcional).
	LogRepo *ProcessingLogRepo
	// Sleep é injetável para testes (padrão: time.Sleep).
	Sleep func(time.Duration)
	// TenantCfgRepo permite ao engine persistir entradas de KB (opcional).
	TenantCfgRepo *TenantConfigRepo
	// Pending — fila de dúvidas de clientes aguardando o admin (ciclo admin→cliente).
	Pending *PendingQuestionRepo
}

// ---------------------------------------------------------------------------
// ConversationEngine
// ---------------------------------------------------------------------------

// ConversationEngine processa mensagens inbound e orquestra o FSM de conversa.
type ConversationEngine struct {
	deps       EngineDeps
	withTenant func(ctx context.Context, fn func(pgx.Tx) error) error
}

// NewConversationEngine cria um engine com as dependências fornecidas.
func NewConversationEngine(deps EngineDeps) *ConversationEngine {
	sleep := deps.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	deps.Sleep = sleep

	return &ConversationEngine{
		deps:       deps,
		withTenant: withTenant(deps.DB, deps.TenantID),
	}
}

// ---------------------------------------------------------------------------
// Handle — ponto de entrada principal
// ---------------------------------------------------------------------------

// Handle processa uma mensagem inbound de ponta a ponta.
// O fluxo completo ocorre dentro de uma única transação por conta do withTenant,
// exceto pelos Sleeps de humanização que acontecem FORA da transação
// (o commit de cada balão anterior já foi realizado antes do Sleep seguinte).
//
// Nota de implementação: os balões são enviados e persistidos sequencialmente,
// cada um dentro de um withTenant separado para que o Sleep ocorra entre eles
// sem manter a transação aberta. O grosso do fluxo (resolução de contato,
// conversa, deduplificação, resposta do LLM) acontece em uma única transação
// inicial; os balões são gravados em transações individuais posteriores.
func (e *ConversationEngine) Handle(ctx context.Context, inbound InboundMessage) error {
	log := e.deps.Logger.With(
		"tenant_id", inbound.TenantID,
		"wamid", inbound.ProviderMessageID,
		"from", inbound.ExternalID,
	)

	// -----------------------------------------------------------------------
	// Fase 1: dentro de uma única transação — resolução, dedup, LLM
	// -----------------------------------------------------------------------

	handleStart := time.Now()

	var (
		conv          Conversation
		cfg           TenantConfig
		output        ResponderOutput
		inboundText   string
		mediaFallback bool
		contactPhone  = inbound.ExternalID
		contactName   string
		convCtx       ConversationContext
		llmReady      bool
	)

	err := e.withTenant(ctx, func(tx pgx.Tx) error {
		// a) Configuração do tenant (necessária antes de criar a conversa)
		tenantCfg, err := e.deps.Config.Get(ctx, tx, inbound.TenantID)
		if err != nil {
			return fmt.Errorf("TenantConfig.Get: %w", err)
		}
		cfg = *tenantCfg

		// b) Resolve contact + channel identity
		contact, chIdentity, err := e.deps.Contacts.FindByChannelIdentity(ctx, tx, inbound.Channel, inbound.ExternalID)
		if err != nil {
			return fmt.Errorf("FindByChannelIdentity: %w", err)
		}
		if contact == nil {
			contact, chIdentity, err = e.deps.Contacts.CreateWithChannelIdentity(ctx, tx, inbound.TenantID, inbound.Channel, inbound.ExternalID, inbound.DisplayHandle)
			if err != nil {
				return fmt.Errorf("CreateWithChannelIdentity: %w", err)
			}
		}
		contactName = contact.DisplayName

		// c) Resolve conversa
		convPtr, err := e.deps.Convs.FindByChannelIdentity(ctx, tx, chIdentity.ID)
		if err != nil {
			return fmt.Errorf("FindByChannelIdentity (conv): %w", err)
		}
		if convPtr == nil {
			convPtr, err = e.deps.Convs.Create(ctx, tx, inbound.TenantID, contact.ID, chIdentity.ID, inbound.Channel, isBotEnabledFor(cfg, inbound.ExternalID))
			if err != nil {
				return fmt.Errorf("Create conversation: %w", err)
			}
		}
		conv = *convPtr

		// d) Humano no controle → ignora
		if !conv.BotEnabled {
			log.Info("bot desabilitado para esta conversa, ignorando mensagem")
			return nil
		}

		// d) Deduplicação da mensagem inbound
		firstTime, err := e.deps.Messages.RecordInbound(ctx, tx, inbound.ProviderMessageID, inbound.TenantID, conv.ID, inbound.Content)
		if err != nil {
			return fmt.Errorf("RecordInbound: %w", err)
		}
		if !firstTime {
			log.Info("mensagem duplicada, ignorando", "wamid", inbound.ProviderMessageID)
			return nil
		}

		// e) Resolve inboundText (texto ou transcrição de áudio)
		switch inbound.Content.Type {
		case "text":
			inboundText = inbound.Content.Text
		case "audio":
			if inbound.Content.Transcript != nil {
				inboundText = *inbound.Content.Transcript
			}
		default:
			if inbound.Content.Caption != nil {
				inboundText = *inbound.Content.Caption
			}
		}

		// f) Mídia sem transcrição → resposta de fallback
		if inboundText == "" {
			mediaFallback = true
			return nil
		}

		// g) Emite domain_event "message.received"
		msgReceivedEvent := DomainEvent{
			TenantID:    inbound.TenantID,
			AggregateID: conv.ID,
			Type:        "message.received",
			Payload: map[string]any{
				"conversation_id": conv.ID,
				"contact_id":      contact.ID,
				"wamid":           inbound.ProviderMessageID,
				"modality":        inbound.Content.Type,
			},
			OccurredAt: inbound.ReceivedAt,
		}
		if err := e.deps.Emitter.Emit(ctx, tx, msgReceivedEvent); err != nil {
			return fmt.Errorf("Emit message.received: %w", err)
		}

		// h) Busca turnos recentes
		recentTurns, err := e.deps.Messages.GetRecentTurns(ctx, tx, conv.ID)
		if err != nil {
			return fmt.Errorf("GetRecentTurns: %w", err)
		}

		// i) Monta ConversationContext
		summary := ""
		if conv.Summary != nil {
			summary = *conv.Summary
		}
		convCtx = ConversationContext{
			RecentTurns:     recentTurns,
			Summary:         summary,
			StructuredFacts: conv.StructuredFacts,
		}

		// k) Quiet hours — verifica se deve suspender o processamento
		if cfg.QuietHoursStart != nil && cfg.QuietHoursEnd != nil {
			hold := QuietHoursHoldMs(inbound.ReceivedAt, cfg.Timezone, *cfg.QuietHoursStart, *cfg.QuietHoursEnd)
			if hold > 0 {
				log.Info("mensagem recebida em quiet hours, agendando retry", "hold", hold)
				nextRetry := inbound.ReceivedAt.Add(hold)
				if err := e.deps.Messages.SetNextRetryAt(ctx, tx, inbound.ProviderMessageID, nextRetry); err != nil {
					return fmt.Errorf("SetNextRetryAt: %w", err)
				}
				return nil
			}
		}

		// l) Detecta conversa admin e sinaliza no cfg (transient). Em modo admin,
		// injeta as dúvidas pendentes para o LLM casar a info e rascunhar respostas.
		if cfg.AdminWhatsAppNumber != "" && contactPhone == cfg.AdminWhatsAppNumber {
			cfg.IsAdminConversation = true
			if e.deps.Pending != nil {
				pendings, err := e.deps.Pending.ListOpen(ctx, tx, inbound.TenantID)
				if err != nil {
					return fmt.Errorf("Pending.ListOpen: %w", err)
				}
				convCtx.PendingQuestions = pendings
			}
		}

		// Sinaliza para chamar o LLM fora da transação.
		llmReady = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("engine.Handle (fase 1): %w", err)
	}

	// Broadcast da mensagem inbound logo após o commit — antes do LLM.
	// O dashboard recebe a mensagem imediatamente sem esperar a resposta.
	if err == nil && inboundText != "" && e.deps.Broadcast != nil {
		e.deps.Broadcast(WSEvent{Type: "message.inbound", ConversationID: conv.ID})
	}

	// Chama o LLM fora da transação (assíncrono ao commit da inbound).
	if llmReady {
		if e.deps.Broadcast != nil {
			e.deps.Broadcast(WSEvent{Type: "conversation.processing", ConversationID: conv.ID})
		}
		respOut, llmErr := e.deps.Responder.Respond(ctx, conv, convCtx, cfg, inboundText)
		if llmErr != nil {
			return fmt.Errorf("engine.Handle (responder): %w", llmErr)
		}
		output = respOut
	}

	// Persiste entrada de KB quando admin forneceu informação (após commit).
	if output.KBEntry != nil && output.KBEntry.Content != "" && e.deps.TenantCfgRepo != nil {
		output.KBEntry.ID = fmt.Sprintf("admin-%d", time.Now().UnixMilli())
		if kbErr := e.deps.TenantCfgRepo.AppendKBEntry(ctx, inbound.TenantID, *output.KBEntry); kbErr != nil {
			log.Warn("engine: falha ao persistir kbEntry do admin", "err", kbErr)
		} else {
			log.Info("engine: entrada KB salva via admin", "title", output.KBEntry.Title)
		}
	}

	// Se não há output (bot desabilitado, dedup, quiet hours, mídia sem texto),
	// trata o caso de fallback de mídia fora da transação e encerra.
	if mediaFallback {
		if err := e.deps.Sender.SendText(ctx, inbound.ExternalID, "recebi sua mídia, em breve respondo"); err != nil {
			log.Error("erro ao enviar fallback de mídia", "err", err)
		}
		return nil
	}

	if len(output.Bubbles) == 0 {
		return nil
	}

	// -----------------------------------------------------------------------
	// Fase 2: envio dos balões (Sleep FORA da transação)
	// -----------------------------------------------------------------------

	wamid := inbound.ProviderMessageID
	prevText := ""

	// Serializa o output do LLM para armazenar como reasoning no primeiro balão.
	var reasoningJSON *string
	if rb, err := json.Marshal(map[string]any{
		"answered":       output.Answered,
		"answeredFromKb": output.AnsweredFromKb,
		"citedEntryIds":  output.CitedEntryIDs,
		"handoff":        output.Handoff,
	}); err == nil {
		s := string(rb)
		reasoningJSON = &s
	}

	for i, bubble := range output.Bubbles {
		// Calcula delay de humanização
		var delay time.Duration
		if i == 0 {
			delay = FirstBubbleDelayMs(bubble)
		} else {
			delay = BetweenBubblesDelayMs(prevText)
		}

		// Sleep acontece fora de qualquer transação
		e.deps.Sleep(delay)

		idempotencyKey := fmt.Sprintf("%s:bubble:%d", wamid, i)

		outboundMsg := OutboundMessage{
			TenantID:       inbound.TenantID,
			ConversationID: conv.ID,
			Channel:        inbound.Channel,
			To:             inbound.ExternalID,
			Intent:         IntentFreeForm,
			Content:        MessageContent{Type: "text", Text: bubble},
			IdempotencyKey: idempotencyKey,
		}

		// Apenas o primeiro balão carrega o reasoning.
		var bubbleReasoning *string
		if i == 0 {
			bubbleReasoning = reasoningJSON
		}

		// Envia e persiste dentro de uma transação individual por balão
		txErr := e.withTenant(ctx, func(tx pgx.Tx) error {
			providerMsgID, err := e.deps.Sender.SendMessage(ctx, outboundMsg)
			if err != nil {
				return fmt.Errorf("SendMessage bubble %d: %w", i, err)
			}

			// Persiste com ON CONFLICT DO NOTHING para exactly-once
			if err := e.deps.Messages.RecordOutbound(ctx, tx, idempotencyKey, inbound.TenantID, conv.ID, providerMsgID, bubble, bubbleReasoning); err != nil {
				return fmt.Errorf("RecordOutbound bubble %d: %w", i, err)
			}

			return nil
		})
		if txErr != nil {
			log.Error("erro ao enviar balão", "index", i, "err", txErr)
			return fmt.Errorf("engine.Handle (balão %d): %w", i, txErr)
		}

		// Broadcast do balão enviado (após commit da transação individual).
		if e.deps.Broadcast != nil {
			e.deps.Broadcast(WSEvent{Type: "message.outbound", ConversationID: conv.ID})
		}

		prevText = bubble
	}

	// -----------------------------------------------------------------------
	// Fase 2.5: modo admin — rascunha/envia respostas a clientes pendentes
	// -----------------------------------------------------------------------
	if len(output.ClientActions) > 0 {
		e.executeClientActions(ctx, inbound, output.ClientActions)
	}

	// -----------------------------------------------------------------------
	// Fase 3: atualiza estado do FSM e emite eventos pós-envio
	// -----------------------------------------------------------------------

	err = e.withTenant(ctx, func(tx pgx.Tx) error {
		// Recarrega conversa para ter estado atual
		convPtr, err := e.deps.Convs.FindByChannelIdentity(ctx, tx, conv.ChannelIdentityID)
		if err != nil {
			return fmt.Errorf("reload conversation: %w", err)
		}
		if convPtr != nil {
			conv = *convPtr
		}

		prevState := conv.State

		// n) Aplica transição do FSM
		newState := applyTransition(conv.State, output)

		// o) Handoff
		if output.Handoff {
			newState = StateHandoff
			handoffEvent := DomainEvent{
				TenantID:    inbound.TenantID,
				AggregateID: conv.ID,
				Type:        "notification.requested",
				Payload: map[string]any{
					"type":            "HANDOFF",
					"conversation_id": conv.ID,
					"contact_id":      conv.ContactID,
				},
				OccurredAt: time.Now(),
			}
			if err := e.deps.Emitter.Emit(ctx, tx, handoffEvent); err != nil {
				return fmt.Errorf("Emit notification.requested (handoff): %w", err)
			}

			// Registra a dúvida do cliente na fila para o admin responder depois.
			// Não aplica a conversas admin (admin não é um cliente esperando).
			if !cfg.IsAdminConversation && inboundText != "" && e.deps.Pending != nil {
				if err := e.deps.Pending.Insert(ctx, tx, inbound.TenantID, conv.ID, contactPhone, contactName, inboundText); err != nil {
					return fmt.Errorf("Pending.Insert: %w", err)
				}
			}
		}

		// p) Reativação agendada
		if output.ScheduledContact != nil {
			if err := e.deps.Convs.SaveReactivation(ctx, tx, conv.ID, inbound.TenantID, conv.ContactID, output.ScheduledContact); err != nil {
				return fmt.Errorf("SaveReactivation: %w", err)
			}
		}

		// q) Gap na KB detectado: cliente fez uma pergunta factual respondida fora da
		// KB. Exclui conversa fiada (saudações) e handoff (que tem fluxo próprio).
		if !output.AnsweredFromKb && output.Answered && !output.Handoff && !output.Smalltalk {
			kbGapEvent := DomainEvent{
				TenantID:    inbound.TenantID,
				AggregateID: conv.ID,
				Type:        "kb.gap_detected",
				Payload: map[string]any{
					"conversation_id": conv.ID,
					"inbound_text":    inboundText,
					"answer_bubbles":  output.Bubbles,
				},
				OccurredAt: time.Now(),
			}
			if err := e.deps.Emitter.Emit(ctx, tx, kbGapEvent); err != nil {
				return fmt.Errorf("Emit kb.gap_detected: %w", err)
			}
		}

		// r) Emite mudança de estado se necessário
		conv.State = newState
		if newState != prevState {
			stateEvent := DomainEvent{
				TenantID:    inbound.TenantID,
				AggregateID: conv.ID,
				Type:        "conversation.state_changed",
				Payload: map[string]any{
					"conversation_id": conv.ID,
					"from":            string(prevState),
					"to":              string(newState),
				},
				OccurredAt: time.Now(),
			}
			if err := e.deps.Emitter.Emit(ctx, tx, stateEvent); err != nil {
				return fmt.Errorf("Emit conversation.state_changed: %w", err)
			}
		}

		// s) Persiste conversa
		now := time.Now()
		conv.LastOutboundAt = &now
		if err := e.deps.Convs.Save(ctx, tx, conv); err != nil {
			return fmt.Errorf("Convs.Save: %w", err)
		}

		return nil
	})
	if err == nil && e.deps.Broadcast != nil {
		e.deps.Broadcast(WSEvent{Type: "conversation.updated", ConversationID: conv.ID})
	}

	// Grava log de processamento (assíncrono para não bloquear o fluxo).
	if inboundText != "" && e.deps.LogRepo != nil {
		answered := output.Answered
		answeredFromKb := output.AnsweredFromKb
		handoff := output.Handoff
		var toolCallsJSON json.RawMessage
		if len(output.ToolCalls) > 0 {
			toolCallsJSON, _ = json.Marshal(output.ToolCalls)
		}
		entry := ProcessingLogEntry{
			TenantID:       inbound.TenantID,
			ConversationID: conv.ID,
			ContactPhone:   contactPhone,
			ContactName:    contactName,
			InboundText:    inboundText,
			Answered:       &answered,
			AnsweredFromKb: &answeredFromKb,
			Handoff:        &handoff,
			CitedEntryIDs:  output.CitedEntryIDs,
			Bubbles:        output.Bubbles,
			ToolCalls:      toolCallsJSON,
			ProcessingMs:   int(time.Since(handleStart).Milliseconds()),
		}
		if err != nil {
			msg := err.Error()
			entry.Error = msg
		}
		go func() {
			if insertErr := e.deps.LogRepo.Insert(context.Background(), entry); insertErr != nil {
				e.deps.Logger.Error("engine: falha ao gravar log de processamento", "err", insertErr)
				return
			}
			if e.deps.Broadcast != nil {
				dl := toDashLog(entry)
				e.deps.Broadcast(WSEvent{Type: "log.new", Log: &dl})
			}
		}()
	}

	return err
}

// ---------------------------------------------------------------------------
// applyTransition — FSM de estado da conversa
// ---------------------------------------------------------------------------

// isBotEnabledFor decide se o bot deve estar ativo para um número recém-chegado,
// de acordo com a config do tenant. Se BotEnabledByDefault=false, o bot só é
// ativado para números presentes em BotAllowedNumbers.
func isBotEnabledFor(cfg TenantConfig, externalID string) bool {
	if cfg.BotEnabledByDefault {
		return true
	}
	for _, n := range cfg.BotAllowedNumbers {
		if n == externalID {
			return true
		}
	}
	return false
}

// applyTransition calcula o próximo estado da conversa dado o estado atual
// e a saída do Responder.
// executeClientActions processa as ações propostas pelo LLM no modo admin sobre
// dúvidas pendentes: grava rascunho (Send=false) ou envia ao cliente (Send=true),
// reengaja a conversa do cliente e resolve a pendência. Cada ação roda numa
// transação própria, fora da conversa do admin.
func (e *ConversationEngine) executeClientActions(ctx context.Context, inbound InboundMessage, actions []ClientAction) {
	log := e.deps.Logger
	if e.deps.Pending == nil || e.deps.Sender == nil {
		return
	}

	for _, act := range actions {
		if act.PendingID == "" {
			continue
		}

		// Carrega a pendência ainda aberta — guarda de idempotência.
		var pq *PendingQuestion
		if err := e.withTenant(ctx, func(tx pgx.Tx) error {
			loaded, e2 := e.deps.Pending.Get(ctx, tx, inbound.TenantID, act.PendingID)
			pq = loaded
			return e2
		}); err != nil {
			log.Error("clientAction: falha ao carregar pendência", "id", act.PendingID, "err", err)
			continue
		}
		if pq == nil {
			continue // já resolvida ou inexistente
		}

		draft := strings.TrimSpace(act.Draft)
		if draft == "" {
			draft = pq.Draft // reaproveita rascunho anterior se o envio veio sem texto
		}
		if draft == "" {
			continue
		}

		// Apenas rascunho: guarda e aguarda a confirmação do admin.
		if !act.Send {
			if err := e.withTenant(ctx, func(tx pgx.Tx) error {
				return e.deps.Pending.StoreDraft(ctx, tx, inbound.TenantID, pq.ID, draft)
			}); err != nil {
				log.Error("clientAction: falha ao guardar rascunho", "id", pq.ID, "err", err)
			}
			continue
		}

		// Envio confirmado: manda ao cliente, persiste, reengaja e resolve.
		idemKey := fmt.Sprintf("pending:%s:reply", pq.ID)
		outboundMsg := OutboundMessage{
			TenantID:       inbound.TenantID,
			ConversationID: pq.ConversationID,
			Channel:        inbound.Channel,
			To:             pq.ClientPhone,
			Intent:         IntentFreeForm,
			Content:        MessageContent{Type: "text", Text: draft},
			IdempotencyKey: idemKey,
		}
		sendErr := e.withTenant(ctx, func(tx pgx.Tx) error {
			providerMsgID, err := e.deps.Sender.SendMessage(ctx, outboundMsg)
			if err != nil {
				return fmt.Errorf("SendMessage: %w", err)
			}
			if err := e.deps.Messages.RecordOutbound(ctx, tx, idemKey, inbound.TenantID, pq.ConversationID, providerMsgID, draft, nil); err != nil {
				return fmt.Errorf("RecordOutbound: %w", err)
			}
			if err := e.deps.Convs.SetState(ctx, tx, inbound.TenantID, pq.ConversationID, StateEngaged); err != nil {
				return fmt.Errorf("SetState: %w", err)
			}
			if err := e.deps.Pending.MarkResolved(ctx, tx, inbound.TenantID, pq.ID); err != nil {
				return fmt.Errorf("MarkResolved: %w", err)
			}
			return nil
		})
		if sendErr != nil {
			log.Error("clientAction: falha ao enviar ao cliente", "id", pq.ID, "err", sendErr)
			who := pq.ClientName
			if who == "" {
				who = pq.ClientPhone
			}
			_ = e.deps.Sender.SendText(ctx, inbound.ExternalID,
				fmt.Sprintf("⚠️ Não consegui enviar a resposta para %s agora. Tenta de novo daqui a pouco.", who))
			continue
		}

		if e.deps.Broadcast != nil {
			e.deps.Broadcast(WSEvent{Type: "message.outbound", ConversationID: pq.ConversationID})
		}
		log.Info("clientAction: resposta enviada ao cliente", "pendingID", pq.ID, "conv", pq.ConversationID)
	}
}

func applyTransition(current ConversationState, output ResponderOutput) ConversationState {
	switch current {
	case StateNew:
		// Primeira resposta: sempre avança para ENGAGED
		return StateEngaged

	case StateEngaged:
		if output.Handoff {
			return StateHandoff
		}
		if output.Answered && output.AnsweredFromKb {
			return StateConcludedPositive
		}
		if output.Answered && !output.AnsweredFromKb {
			// Respondeu, mas sem KB → aguarda complemento humano
			return StateAwaitingReply
		}
		return StateEngaged

	case StateAwaitingReply:
		// Nova mensagem chegou → volta para engajado (resetado pelo Handle)
		return StateEngaged

	case StateHandoff:
		// Só humano pode mudar este estado
		return StateHandoff

	default:
		return current
	}
}
