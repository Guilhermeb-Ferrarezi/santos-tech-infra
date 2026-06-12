package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// WorkerDeps agrupa todas as dependências do worker de background.
type WorkerDeps struct {
	Config Config
	Logger *slog.Logger

	// Pool de conexões Postgres (campo DB ou Pool — ambos aceitos para compatibilidade).
	DB   *pgxpool.Pool
	Pool *pgxpool.Pool

	// Redis (não usado ativamente no MVP — reservado para feature futura).
	Redis *redis.Client

	// Repos
	Outbox            *OutboxRepo
	ScheduledContacts *ScheduledContactRepo

	// Atalhos derivados (populados em NewWorker se ausentes).
	Scheduled *ScheduledContactRepo
	Convs     *ConversationRepo
	Contacts  *ContactRepo
	AgentGo   *AgentGoClient
	TenantCfg *TenantConfigRepo
	Sender    ChatSender
}

// Worker processa o outbox de domain events e os follow-ups agendados.
type Worker struct {
	deps WorkerDeps
}

// NewWorker cria um Worker com as dependências fornecidas.
// Normaliza aliases (Pool/DB, ScheduledContacts/Scheduled) para evitar nil panics.
func NewWorker(deps WorkerDeps) *Worker {
	// Normaliza pool
	if deps.Pool == nil {
		deps.Pool = deps.DB
	}
	if deps.DB == nil {
		deps.DB = deps.Pool
	}

	// Normaliza ScheduledContacts/Scheduled
	if deps.Scheduled == nil {
		deps.Scheduled = deps.ScheduledContacts
	}
	if deps.ScheduledContacts == nil {
		deps.ScheduledContacts = deps.Scheduled
	}

	// Cria repos derivados automaticamente se pool disponível e repos ausentes
	if deps.Pool != nil {
		if deps.Convs == nil {
			deps.Convs = NewConversationRepo(deps.Pool)
		}
		if deps.Contacts == nil {
			deps.Contacts = NewContactRepo(deps.Pool)
		}
		if deps.Outbox == nil {
			deps.Outbox = NewOutboxRepo(deps.Pool)
		}
		if deps.TenantCfg == nil {
			deps.TenantCfg = NewTenantConfigRepo(deps.Pool)
		}
	}

	return &Worker{deps: deps}
}

// Start inicia as goroutines do worker e bloqueia até o contexto ser cancelado.
func (w *Worker) Start(ctx context.Context) {
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		w.outboxLoop(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		w.followUpLoop(ctx)
	}()

	<-ctx.Done()
	w.deps.Logger.Info("worker: contexto cancelado, aguardando goroutines")
	wg.Wait()
	w.deps.Logger.Info("worker: encerrado")
}

// outboxLoop drena o outbox de domain events em loop.
func (w *Worker) outboxLoop(ctx context.Context) {
	cfg := w.deps.Config
	sem := make(chan struct{}, cfg.OutboxBatchSize)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		events, err := w.deps.Outbox.Drain(ctx, cfg.OutboxBatchSize)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.deps.Logger.Error("outboxLoop: erro ao drenar outbox", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(cfg.OutboxIdleIntervalMs) * time.Millisecond):
			}
			continue
		}

		var wg sync.WaitGroup
		for _, ev := range events {
			ev := ev
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				w.processEvent(ctx, ev)
			}()
		}
		wg.Wait()

		if len(events) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(cfg.OutboxIdleIntervalMs) * time.Millisecond):
			}
		}
		// se len(events) == batchSize, continua sem sleep (pode haver mais)
	}
}

// processEvent processa um DomainEvent do outbox.
func (w *Worker) processEvent(ctx context.Context, ev DomainEvent) {
	var processingErr error

	defer func() {
		if r := recover(); r != nil {
			processingErr = fmt.Errorf("panic: %v", r)
		}
		if processingErr != nil {
			w.deps.Logger.Error("processEvent: falha ao processar evento",
				"eventID", ev.ID, "type", ev.Type, "err", processingErr)
			if err := w.deps.Outbox.MarkFailed(ctx, ev.ID, processingErr.Error()); err != nil {
				w.deps.Logger.Error("processEvent: erro ao marcar evento como falho",
					"eventID", ev.ID, "err", err)
			}
			return
		}
		if err := w.deps.Outbox.MarkProcessed(ctx, ev.ID); err != nil {
			w.deps.Logger.Error("processEvent: erro ao marcar evento como processado",
				"eventID", ev.ID, "err", err)
		}
	}()

	switch ev.Type {
	case "message.received":
		processingErr = w.handleMessageReceived(ctx, ev)

	case "message.sent":
		processingErr = w.handleMessageSent(ctx, ev)

	case "kb.gap_detected":
		if w.deps.Config.AdminWhatsAppNumber != "" {
			processingErr = w.notificaAdmin(ctx, ev)
		}
		if processingErr == nil && w.deps.AgentGo != nil && w.deps.TenantCfg != nil {
			if err := w.handleKBGap(ctx, ev); err != nil {
				w.deps.Logger.Warn("handleKBGap: falha ao persistir entrada KB", "err", err)
			}
		}

	case "notification.requested":
		if w.deps.Config.AdminWhatsAppNumber != "" {
			processingErr = w.notificaAdmin(ctx, ev)
		}

	default:
		w.deps.Logger.Debug("processEvent: tipo de evento ignorado", "type", ev.Type, "eventID", ev.ID)
	}
}

// handleMessageReceived agenda um follow-up quando uma mensagem é recebida.
func (w *Worker) handleMessageReceived(ctx context.Context, ev DomainEvent) error {
	convID, _ := ev.Payload["conversationId"].(string)
	if convID == "" {
		convID, _ = ev.Payload["conversation_id"].(string)
	}
	if convID == "" {
		return fmt.Errorf("message.received: conversationId ausente no payload")
	}

	contactID, _ := ev.Payload["contactId"].(string)
	if contactID == "" {
		contactID, _ = ev.Payload["contact_id"].(string)
	}

	scheduledAt := time.Now().Add(30 * time.Minute)
	if err := w.deps.Scheduled.ScheduleFollowUp(ctx, ev.TenantID, contactID, convID, scheduledAt); err != nil {
		return fmt.Errorf("handleMessageReceived: ScheduleFollowUp: %w", err)
	}

	w.deps.Logger.Debug("handleMessageReceived: follow-up agendado",
		"convID", convID, "scheduledAt", scheduledAt)
	return nil
}

// handleMessageSent cancela follow-ups pendentes quando o bot responde.
func (w *Worker) handleMessageSent(ctx context.Context, ev DomainEvent) error {
	convID, _ := ev.Payload["conversationId"].(string)
	if convID == "" {
		convID, _ = ev.Payload["conversation_id"].(string)
	}
	if convID == "" {
		return fmt.Errorf("message.sent: conversationId ausente no payload")
	}

	if err := w.deps.Scheduled.CancelFollowUps(ctx, ev.TenantID, convID); err != nil {
		return fmt.Errorf("handleMessageSent: CancelFollowUps: %w", err)
	}

	w.deps.Logger.Debug("handleMessageSent: follow-ups cancelados", "convID", convID)
	return nil
}

// notificaAdmin envia uma notificação para o número de WhatsApp do admin.
func (w *Worker) notificaAdmin(ctx context.Context, ev DomainEvent) error {
	question, _ := ev.Payload["question"].(string)
	if question == "" {
		question, _ = ev.Payload["message"].(string)
	}

	var texto string
	if ev.Type == "kb.gap_detected" {
		texto = fmt.Sprintf("⚠️ Lacuna de conhecimento detectada. Pergunta: %s", question)
	} else {
		texto = fmt.Sprintf("⚠️ Notificação: %s", question)
	}

	if err := w.deps.Sender.SendText(ctx, w.deps.Config.AdminWhatsAppNumber, texto); err != nil {
		return fmt.Errorf("notificaAdmin: SendText: %w", err)
	}

	w.deps.Logger.Info("notificaAdmin: notificação enviada ao admin", "eventType", ev.Type)
	return nil
}

// handleKBGap gera uma entrada de KB a partir da pergunta e resposta do bot e a persiste.
func (w *Worker) handleKBGap(ctx context.Context, ev DomainEvent) error {
	question, _ := ev.Payload["inbound_text"].(string)
	if question == "" {
		return nil
	}

	// Montar texto da resposta a partir dos bubbles
	var answerParts []string
	if bubblesRaw, ok := ev.Payload["answer_bubbles"].([]any); ok {
		for _, b := range bubblesRaw {
			if s, ok := b.(string); ok {
				answerParts = append(answerParts, s)
			}
		}
	}
	if len(answerParts) == 0 {
		return nil
	}
	answer := strings.Join(answerParts, " ")

	prompt := fmt.Sprintf(
		"Você é um sistema de extração de conhecimento. A partir de uma pergunta e sua resposta, "+
			"gere uma entrada para uma base de conhecimento empresarial.\n\n"+
			"Pergunta: %s\nResposta do assistente: %s\n\n"+
			"Retorne SOMENTE JSON válido, sem texto adicional:\n"+
			`{"title":"<título conciso, máximo 60 chars>","content":"<fato factual completo, prosa clara, reutilizável>"}`,
		question, answer,
	)

	raw, err := w.deps.AgentGo.RespondWithModel(ctx, prompt, "haiku", false)
	if err != nil {
		return fmt.Errorf("handleKBGap: gerar entrada: %w", err)
	}

	// Extrair JSON da resposta (pode ter texto extra)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end <= start {
		return fmt.Errorf("handleKBGap: JSON não encontrado na resposta do modelo")
	}
	raw = raw[start : end+1]

	var entry kbEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return fmt.Errorf("handleKBGap: parse entry: %w", err)
	}
	if entry.Title == "" || entry.Content == "" {
		return nil
	}
	entry.ID = fmt.Sprintf("auto-%d", time.Now().UnixMilli())

	if err := w.deps.TenantCfg.AppendKBEntry(ctx, ev.TenantID, entry); err != nil {
		return fmt.Errorf("handleKBGap: persistir: %w", err)
	}

	w.deps.Logger.Info("handleKBGap: entrada KB criada", "title", entry.Title)
	return nil
}

// followUpLoop processa follow-ups agendados a cada 60 segundos.
func (w *Worker) followUpLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	// executa imediatamente na primeira vez
	w.runFollowUps(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runFollowUps(ctx)
		}
	}
}

// runFollowUps busca e processa os follow-ups pendentes em paralelo.
func (w *Worker) runFollowUps(ctx context.Context) {
	cfg := w.deps.Config
	rows, err := w.deps.Scheduled.PendingFollowUps(ctx, cfg.FollowUpConcurrency)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		w.deps.Logger.Error("followUpLoop: erro ao buscar follow-ups pendentes", "err", err)
		return
	}

	if len(rows) == 0 {
		return
	}

	sem := make(chan struct{}, cfg.FollowUpConcurrency)
	var wg sync.WaitGroup

	for _, row := range rows {
		row := row
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			w.processFollowUp(ctx, row)
		}()
	}
	wg.Wait()
}

// processFollowUp executa o envio de um follow-up agendado.
func (w *Worker) processFollowUp(ctx context.Context, row ScheduledContactRow) {
	cfg := w.deps.Config
	log := w.deps.Logger.With("followUpID", row.ID, "convID", row.ConversationID)

	handleErr := func(err error) {
		log.Error("processFollowUp: falha", "err", err, "attempts", row.Attempts)
		if row.Attempts+1 < cfg.OutboxMaxAttempts {
			if mErr := w.deps.Scheduled.MarkFollowUpFailed(ctx, row.ID, err.Error()); mErr != nil {
				log.Error("processFollowUp: erro ao marcar follow-up como falho", "err", mErr)
			}
		} else {
			log.Warn("processFollowUp: máximo de tentativas atingido, descartando", "maxAttempts", cfg.OutboxMaxAttempts)
			if mErr := w.deps.Scheduled.MarkFollowUpFailed(ctx, row.ID, fmt.Sprintf("max attempts reached: %s", err)); mErr != nil {
				log.Error("processFollowUp: erro ao marcar follow-up como falho (max)", "err", mErr)
			}
		}
	}

	// 1. Busca a conversa
	conv, err := w.deps.Convs.FindByID(ctx, row.TenantID, row.ConversationID)
	if err != nil {
		handleErr(fmt.Errorf("FindByID conv: %w", err))
		return
	}

	// 2. Se o bot está desabilitado, pula
	if !conv.BotEnabled {
		log.Debug("processFollowUp: bot desabilitado, pulando")
		if err := w.deps.Scheduled.MarkFollowUpFailed(ctx, row.ID, "bot disabled"); err != nil {
			log.Error("processFollowUp: erro ao marcar skip (bot disabled)", "err", err)
		}
		return
	}

	// 3. Se está em handoff, pula
	if conv.State == StateHandoff {
		log.Debug("processFollowUp: conversa em handoff, pulando")
		if err := w.deps.Scheduled.MarkFollowUpFailed(ctx, row.ID, "handoff state"); err != nil {
			log.Error("processFollowUp: erro ao marcar skip (handoff)", "err", err)
		}
		return
	}

	// 4. Busca a identidade de canal (telefone)
	identity, err := w.deps.Contacts.FindChannelIdentity(ctx, row.TenantID, row.ContactID)
	if err != nil {
		handleErr(fmt.Errorf("FindChannelIdentity: %w", err))
		return
	}

	// 5. Verifica janela de 24h
	now := time.Now()
	withinWindow := conv.LastInboundAt != nil && now.Sub(*conv.LastInboundAt) < 24*time.Hour

	if withinWindow {
		// Dentro da janela: envia mensagem de texto livre gerada pelo LLM
		summaryCtx := ""
		if conv.Summary != nil && *conv.Summary != "" {
			summaryCtx = " Contexto: " + *conv.Summary
		}
		prompt := fmt.Sprintf(
			"Gere uma mensagem curta e natural de follow-up para um cliente que não respondeu.%s Seja gentil e direto.",
			summaryCtx,
		)

		text, err := w.deps.AgentGo.RespondWithModel(ctx, prompt, "haiku", false)
		if err != nil {
			handleErr(fmt.Errorf("RespondWithModel: %w", err))
			return
		}

		msg := OutboundMessage{
			TenantID:       row.TenantID,
			ConversationID: row.ConversationID,
			Channel:        conv.Channel,
			To:             identity.ExternalID,
			Intent:         IntentFreeForm,
			Content:        MessageContent{Type: "text", Text: text},
			IdempotencyKey: fmt.Sprintf("followup-%s", row.ID),
		}

		if _, err := w.deps.Sender.SendMessage(ctx, msg); err != nil {
			handleErr(fmt.Errorf("SendMessage FREE_FORM: %w", err))
			return
		}

		log.Info("processFollowUp: follow-up FREE_FORM enviado")
	} else {
		// Fora da janela 24h: requer template aprovado
		if !cfg.FollowUpHasApprovedTemplates {
			log.Info("processFollowUp: fora da janela 24h e sem template aprovado, pulando")
			if err := w.deps.Scheduled.MarkFollowUpFailed(ctx, row.ID, "outside 24h window, no approved template"); err != nil {
				log.Error("processFollowUp: erro ao marcar skip (sem template)", "err", err)
			}
			return
		}

		vars := row.TemplateVars
		if vars == nil {
			vars = map[string]string{}
		}

		msg := OutboundMessage{
			TenantID:       row.TenantID,
			ConversationID: row.ConversationID,
			Channel:        conv.Channel,
			To:             identity.ExternalID,
			Intent:         IntentStructuredReengagement,
			Content:        MessageContent{Type: "template"},
			IdempotencyKey: fmt.Sprintf("followup-tmpl-%s", row.ID),
			TemplatePayload: &TemplatePayload{
				Name:      cfg.FollowUpTemplateName,
				Language:  cfg.FollowUpTemplateLanguage,
				Variables: vars,
			},
		}

		if _, err := w.deps.Sender.SendMessage(ctx, msg); err != nil {
			handleErr(fmt.Errorf("SendMessage STRUCTURED_REENGAGEMENT: %w", err))
			return
		}

		log.Info("processFollowUp: follow-up STRUCTURED_REENGAGEMENT enviado")
	}

	// 6. Marca como enviado
	if err := w.deps.Scheduled.MarkFollowUpSent(ctx, row.ID); err != nil {
		log.Error("processFollowUp: erro ao marcar follow-up como enviado", "err", err)
	}
}
