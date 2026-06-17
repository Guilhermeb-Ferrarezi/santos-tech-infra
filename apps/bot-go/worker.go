package main

import (
	"context"
	"encoding/json"
	"errors"
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
	// Webhook — usado pelo drenador de retries (mensagens retidas em quiet hours).
	Webhook *WebhookRepo

	// RetryStream — consumidor do Redis Stream de retries (reprocesso quase em
	// tempo real). Pode ser nil (Redis ausente) → só o polling roda.
	RetryStream *RetryStream

	// Engines para reprocessar mensagens retidas (quiet hours) quando o retry vence.
	// Engine = canal oficial (Meta); EvoEngine = canal não-oficial (Evolution).
	Engine    *ConversationEngine
	EvoEngine *ConversationEngine

	// Atalhos derivados (populados em NewWorker se ausentes).
	Scheduled *ScheduledContactRepo
	Convs     *ConversationRepo
	Contacts  *ContactRepo
	AgentGo   *AgentGoClient
	TenantCfg *TenantConfigRepo
	Sender    ChatSender
	// EvolutionSender — sender do canal não-oficial (Evolution). Usado para notificar
	// o admin pelo MESMO canal de origem do cliente (quando payload.channel='evolution'),
	// para que a resposta do admin volte e seja roteada pelo canal certo.
	EvolutionSender ChatSender
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

	wg.Add(1)
	go func() {
		defer wg.Done()
		w.retryLoop(ctx)
	}()

	// Consumidor do Redis Stream de retries (reprocesso quase em tempo real).
	// O retryLoop acima permanece como SAFETY NET (intervalo maior). Só sobe se
	// houver stream + webhook + engine.
	if w.deps.RetryStream != nil && w.deps.RetryStream.enabled() &&
		w.deps.Webhook != nil && w.deps.Engine != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.deps.RetryStream.Consume(ctx, w.handleStreamRetry)
		}()
	}

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
		// Gaps de KB são salvos silenciosamente — sem notificar o admin aqui.
		// O admin só é avisado quando há cliente esperando de verdade (handoff,
		// via notification.requested), que tem o fluxo de rascunho + confirmação.
		if w.deps.AgentGo != nil && w.deps.TenantCfg != nil {
			if err := w.handleKBGap(ctx, ev); err != nil {
				w.deps.Logger.Warn("handleKBGap: falha ao persistir entrada KB", "err", err)
			}
		}

	case "notification.requested":
		processingErr = w.notificaAdmin(ctx, ev)

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
	if w.deps.TenantCfg == nil {
		return nil
	}
	cfg, err := w.deps.TenantCfg.Get(ctx, nil, ev.TenantID)
	if err != nil {
		return nil
	}
	admins := cfg.AdminNumbers()
	if len(admins) == 0 {
		return nil
	}

	// Escolhe o sender conforme o canal de ORIGEM do cliente (gravado no payload).
	// Se o cliente veio pela Evolution, o admin é avisado pela Evolution — assim a
	// resposta do admin volta pelo mesmo canal e é roteada ao cliente corretamente.
	channel, _ := ev.Payload["channel"].(string)
	notifSender := w.deps.Sender
	if channel == "evolution" && w.deps.EvolutionSender != nil {
		notifSender = w.deps.EvolutionSender
	}
	if notifSender == nil {
		return nil
	}

	question, _ := ev.Payload["question"].(string)
	if question == "" {
		question, _ = ev.Payload["message"].(string)
	}
	if question == "" {
		question, _ = ev.Payload["inbound_text"].(string)
	}

	var texto string
	switch {
	case ev.Type == "kb.gap_detected":
		texto = fmt.Sprintf(
			"📚 *Pergunta sem resposta na base de conhecimento:*\n\n_%s_\n\nResponda esta mensagem com a informação correta e eu salvo na KB automaticamente.",
			question,
		)
	default:
		if ntype, _ := ev.Payload["type"].(string); ntype == "BOOKING" {
			texto = "📅 *Novo pedido de agendamento:*\n\n" + question
		} else {
			texto = fmt.Sprintf("🔔 *Cliente aguardando atendimento humano:*\n\n_%s_", question)
		}
	}

	var firstErr error
	sent := 0
	for _, admin := range admins {
		if err := notifSender.SendText(ctx, admin, texto); err != nil {
			w.deps.Logger.Error("notificaAdmin: falha ao enviar a um admin", "admin", admin, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		sent++
	}
	// Só falha (para retry) se NENHUM admin recebeu; sucesso parcial não reprocessa.
	if sent == 0 && firstErr != nil {
		return fmt.Errorf("notificaAdmin: SendText: %w", firstErr)
	}

	w.deps.Logger.Info("notificaAdmin: notificação enviada", "eventType", ev.Type, "admins", sent)
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

	var entry KBEntry
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

// ---------------------------------------------------------------------------
// retryLoop — drena mensagens RETIDAS (quiet hours) e as reprocessa
// ---------------------------------------------------------------------------

// retryLoop reprocessa, a cada 60s, as mensagens retidas em quiet hours. Espelha
// o followUpLoop. O engine, ao receber uma mensagem em quiet hours, grava o
// webhook_event como 'failed' + next_retry_at (via SetNextRetryAt). Quando
// next_retry_at vence, este loop redrena e re-invoca o engine — fechando o ciclo
// que antes ficava órfão (mensagem fora do horário sumia para sempre).
func (w *Worker) retryLoop(ctx context.Context) {
	// Sem repo de webhook ou engine, não há o que drenar (ex.: testes do worker).
	if w.deps.Webhook == nil || w.deps.Engine == nil {
		return
	}

	// Safety net: com o Redis Stream cobrindo o caminho quente, o polling pode
	// ser bem mais espaçado (default 5min via RetryPollInterval). Cobre eventos
	// órfãos (stream fora, publish perdido) sem martelar o Postgres. Mantém 60s
	// se o intervalo não estiver configurado (compat. com testes/zero-value).
	interval := w.deps.Config.RetryPollInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	w.runRetries(ctx) // imediato na primeira vez

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runRetries(ctx)
		}
	}
}

// runRetries busca os webhooks 'failed' vencidos e reprocessa cada um.
func (w *Worker) runRetries(ctx context.Context) {
	const limit = 20
	events, err := w.deps.Webhook.PendingRetries(ctx, limit)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		w.deps.Logger.Error("retryLoop: erro ao buscar retries pendentes", "err", err)
		return
	}
	for _, ev := range events {
		w.processRetry(ctx, ev)
	}
}

// handleStreamRetry é o handler do consumidor do Redis Stream. Carrega o
// webhook pelo ID (o stream só transporta o ID), checa se ainda é elegível para
// retry e o reprocessa via processRetry (que já marca done/failed no banco).
//
// Retorna erro APENAS em falha transitória (não conseguiu carregar o evento) —
// nesse caso o RetryStream NÃO dá XACK e a entrada é reentregue (at-least-once),
// e o polling do Postgres é a rede de segurança final. Em qualquer desfecho
// terminal (processado, descartado, não-elegível), retorna nil → XACK.
func (w *Worker) handleStreamRetry(ctx context.Context, webhookID WebhookEventID) error {
	ev, err := w.deps.Webhook.GetByID(ctx, webhookID)
	if err != nil {
		// Erro transitório no banco: não ACKa, deixa reentregar.
		return fmt.Errorf("handleStreamRetry: GetByID: %w", err)
	}
	if ev == nil {
		// Evento sumiu (limpeza/descartado): nada a fazer, ACK.
		return nil
	}
	if ev.Status != "failed" {
		// Já resolvido (done) por outro caminho, ou ainda não elegível: ACK e
		// deixa o polling pegar quando next_retry_at vencer, se for o caso.
		w.deps.Logger.Debug("handleStreamRetry: webhook não está 'failed', ignorando",
			"webhookID", webhookID, "status", ev.Status)
		return nil
	}
	// processRetry cuida do MarkDone/MarkFailed; aqui o desfecho é sempre
	// terminal para fins de ACK (o estado de retry persiste no banco).
	w.processRetry(ctx, *ev)
	return nil
}

// processRetry reconstrói a InboundMessage de um webhook retido e re-invoca o engine
// do canal correspondente. Em sucesso, marca o webhook 'done'; se ainda estiver em
// quiet hours (held), o SetNextRetryAt já reagendou (deixa como está); em erro real,
// MarkFailed aplica novo backoff.
func (w *Worker) processRetry(ctx context.Context, ev WebhookEvent) {
	log := w.deps.Logger.With("webhookID", ev.ID, "provider", ev.Provider)

	inbound, engine, ok := w.rebuildInbound(ev)
	if !ok {
		// Não foi possível reconstruir (payload inesperado ou canal sem engine):
		// marca done para não ficar em loop eterno de retry.
		log.Warn("retryLoop: não foi possível reconstruir inbound, descartando")
		if err := w.deps.Webhook.MarkDone(ctx, ev.ID); err != nil {
			log.Error("retryLoop: erro ao descartar webhook", "err", err)
		}
		return
	}

	err := engine.Handle(ctx, inbound)
	switch {
	case errors.Is(err, ErrMessageHeld):
		// Ainda em quiet hours: o engine já reagendou o next_retry_at. Não toca.
		log.Debug("retryLoop: mensagem ainda retida, aguardando próximo retry")
	case err != nil:
		log.Error("retryLoop: reprocessamento falhou", "err", err)
		if mErr := w.deps.Webhook.MarkFailed(ctx, ev.ID, err.Error()); mErr != nil {
			log.Error("retryLoop: erro ao marcar webhook failed", "err", mErr)
		}
	default:
		if mErr := w.deps.Webhook.MarkDone(ctx, ev.ID); mErr != nil {
			log.Error("retryLoop: erro ao marcar webhook done", "err", mErr)
		}
		log.Info("retryLoop: mensagem retida reprocessada com sucesso")
	}
}

// rebuildInbound reconstrói a InboundMessage a partir do raw_payload guardado e
// escolhe o engine do canal. Retorna ok=false quando não dá para reconstruir.
func (w *Worker) rebuildInbound(ev WebhookEvent) (InboundMessage, *ConversationEngine, bool) {
	switch ev.Provider {
	case "evolution":
		if w.deps.EvoEngine == nil {
			return InboundMessage{}, nil, false
		}
		var wh evolutionWebhook
		if err := json.Unmarshal(ev.RawPayload, &wh); err != nil {
			return InboundMessage{}, nil, false
		}
		phone := wh.Data.Key.RemoteJid
		if i := strings.IndexAny(phone, "@:"); i > 0 {
			phone = phone[:i]
		}
		text := wh.Data.Message.Conversation
		if text == "" {
			text = wh.Data.Message.ExtendedTextMessage.Text
		}
		if phone == "" || text == "" {
			return InboundMessage{}, nil, false
		}
		return InboundMessage{
			TenantID:          ev.TenantID,
			Channel:           "evolution",
			ExternalID:        phone,
			DisplayHandle:     wh.Data.PushName,
			ProviderMessageID: ev.ProviderEventID,
			Content:           MessageContent{Type: "text", Text: text},
			ReceivedAt:        time.Now(),
		}, w.deps.EvoEngine, true

	default: // 'whatsapp' (Meta)
		msgs, err := ParseMetaWebhook(ev.RawPayload, w.deps.Config.MetaPhoneNumberID)
		if err != nil || len(msgs) == 0 {
			return InboundMessage{}, nil, false
		}
		m := msgs[0]
		m.TenantID = ev.TenantID
		return m, w.deps.Engine, true
	}
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
