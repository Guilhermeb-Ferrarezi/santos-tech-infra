package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ============================================================================
// Construtores
// ============================================================================

func NewContactRepo(pool *pgxpool.Pool) *ContactRepo           { return &ContactRepo{pool: pool} }
func NewConversationRepo(pool *pgxpool.Pool) *ConversationRepo { return &ConversationRepo{pool: pool} }
func NewMessageRepo(pool *pgxpool.Pool) *MessageRepo           { return &MessageRepo{pool: pool} }
func NewLeadRepo(pool *pgxpool.Pool) *LeadRepo                 { return &LeadRepo{pool: pool} }
func NewOutboxRepo(pool *pgxpool.Pool) *OutboxRepo             { return &OutboxRepo{pool: pool} }
func NewWebhookRepo(pool *pgxpool.Pool) *WebhookRepo           { return &WebhookRepo{pool: pool} }
func NewTenantConfigRepo(pool *pgxpool.Pool) *TenantConfigRepo { return &TenantConfigRepo{pool: pool} }
func NewScheduledContactRepo(pool *pgxpool.Pool) *ScheduledContactRepo {
	return &ScheduledContactRepo{pool: pool}
}

// ============================================================================
// ContactRepo
// ============================================================================

type ContactRepo struct{ pool *pgxpool.Pool }

// FindByChannelIdentity encontra um Contact e sua ChannelIdentity pelo canal e
// external_id dentro de uma transação (que já tem o SET LOCAL app.tenant_id).
func (r *ContactRepo) FindByChannelIdentity(ctx context.Context, tx pgx.Tx, channel, externalID string) (*Contact, *ChannelIdentity, error) {
	row := tx.QueryRow(ctx, `
		SELECT c.id, c.tenant_id, c.display_name, c.communication_style, c.created_at, c.updated_at,
		       ci.id, ci.tenant_id, ci.contact_id, ci.channel::text, ci.external_id, ci.created_at
		FROM contact c
		JOIN channel_identity ci ON ci.contact_id = c.id AND ci.tenant_id = c.tenant_id
		WHERE ci.channel = $1::channel
		  AND ci.external_id = $2
	`, channel, externalID)

	var c Contact
	var ci ChannelIdentity
	var style *string
	var channelText string

	err := row.Scan(
		&c.ID, &c.TenantID, &c.DisplayName, &style, &c.CreatedAt, &c.UpdatedAt,
		&ci.ID, &ci.TenantID, &ci.ContactID, &channelText, &ci.ExternalID, &ci.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("ContactRepo.FindByChannelIdentity: %w", err)
	}
	if style != nil {
		s := CommunicationStyle(*style)
		c.CommunicationStyle = &s
	}
	ci.Channel = channelText
	return &c, &ci, nil
}

// CreateWithChannelIdentity cria um novo Contact e uma ChannelIdentity associada
// dentro de uma transação com SET LOCAL app.tenant_id já definido.
func (r *ContactRepo) CreateWithChannelIdentity(ctx context.Context, tx pgx.Tx, tenantID TenantID, channel, externalID, displayHandle string) (*Contact, *ChannelIdentity, error) {
	var c Contact
	c.TenantID = tenantID
	c.DisplayName = displayHandle

	err := tx.QueryRow(ctx, `
		INSERT INTO contact (tenant_id, display_name)
		VALUES ($1, $2)
		RETURNING id, tenant_id, display_name, created_at, updated_at
	`, tenantID, displayHandle).Scan(&c.ID, &c.TenantID, &c.DisplayName, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, nil, fmt.Errorf("ContactRepo.CreateWithChannelIdentity insert contact: %w", err)
	}

	var ci ChannelIdentity
	err = tx.QueryRow(ctx, `
		INSERT INTO channel_identity (tenant_id, contact_id, channel, external_id, display_handle)
		VALUES ($1, $2, $3::channel, $4, $5)
		ON CONFLICT (tenant_id, channel, external_id) DO UPDATE
			SET display_handle = EXCLUDED.display_handle
		RETURNING id, tenant_id, contact_id, channel::text, external_id, created_at
	`, tenantID, c.ID, channel, externalID, displayHandle).Scan(
		&ci.ID, &ci.TenantID, &ci.ContactID, &ci.Channel, &ci.ExternalID, &ci.CreatedAt,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("ContactRepo.CreateWithChannelIdentity insert channel_identity: %w", err)
	}

	return &c, &ci, nil
}

// FindChannelIdentity busca a identidade de canal de um contact (para follow-ups).
// Busca a primeira channel_identity do contact (por criação mais recente).
func (r *ContactRepo) FindChannelIdentity(ctx context.Context, tenantID TenantID, contactID ContactID) (*ChannelIdentity, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, contact_id, channel::text, external_id, created_at
		FROM channel_identity
		WHERE tenant_id = $1 AND contact_id = $2
		ORDER BY created_at ASC
		LIMIT 1
	`, tenantID, contactID)

	var ci ChannelIdentity
	err := row.Scan(&ci.ID, &ci.TenantID, &ci.ContactID, &ci.Channel, &ci.ExternalID, &ci.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("ContactRepo.FindChannelIdentity: contato %s sem channel_identity", contactID)
	}
	if err != nil {
		return nil, fmt.Errorf("ContactRepo.FindChannelIdentity: %w", err)
	}
	return &ci, nil
}

// SetCommunicationStyle atualiza o estilo de comunicação do contato.
func (r *ContactRepo) SetCommunicationStyle(ctx context.Context, tx pgx.Tx, tenantID TenantID, contactID ContactID, style CommunicationStyle) error {
	_, err := tx.Exec(ctx, `
		UPDATE contact SET communication_style = $1, updated_at = now()
		WHERE tenant_id = $2 AND id = $3
	`, string(style), tenantID, contactID)
	if err != nil {
		return fmt.Errorf("ContactRepo.SetCommunicationStyle: %w", err)
	}
	return nil
}

// SetLeadSummary grava o resumo rolling do lead em lead_summary (upsert).
func (r *ContactRepo) SetLeadSummary(ctx context.Context, tx pgx.Tx, tenantID TenantID, contactID ContactID, summary string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO lead_summary (tenant_id, contact_id, summary)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, contact_id) DO UPDATE SET summary = EXCLUDED.summary, updated_at = now()
	`, tenantID, contactID, summary)
	if err != nil {
		return fmt.Errorf("ContactRepo.SetLeadSummary: %w", err)
	}
	return nil
}

// ============================================================================
// ConversationRepo
// ============================================================================

type ConversationRepo struct{ pool *pgxpool.Pool }

// FindByChannelIdentity encontra a conversa ativa para um channel_identity
// dentro de uma transação (que tem SET LOCAL app.tenant_id).
func (r *ConversationRepo) FindByChannelIdentity(ctx context.Context, tx pgx.Tx, channelIdentityID ChannelIdentityID) (*Conversation, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, tenant_id, channel_identity_id, channel::text, state::text, bot_enabled,
		       last_inbound_at, last_outbound_at, last_inbound_modality,
		       created_at, updated_at
		FROM conversation
		WHERE channel_identity_id = $1
	`, channelIdentityID)

	return scanConversation(row)
}

// FindByID retorna uma conversa pelo ID (sem transação — usada pelo worker).
func (r *ConversationRepo) FindByID(ctx context.Context, tenantID TenantID, convID ConversationID) (*Conversation, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, channel_identity_id, channel::text, state::text, bot_enabled,
		       last_inbound_at, last_outbound_at, last_inbound_modality,
		       created_at, updated_at
		FROM conversation
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, convID)

	return scanConversation(row)
}

func scanConversation(row pgx.Row) (*Conversation, error) {
	var c Conversation
	var modality *string
	var state, channel string
	err := row.Scan(
		&c.ID, &c.TenantID, &c.ChannelIdentityID, &channel,
		&state, &c.BotEnabled,
		&c.LastInboundAt, &c.LastOutboundAt, &modality,
		&c.CreatedAt, &c.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanConversation: %w", err)
	}
	c.State = ConversationState(state)
	c.Channel = channel
	if modality != nil {
		c.LastInboundModality = *modality
	}
	return &c, nil
}

// Create cria uma nova conversa com state=NEW e bot_enabled conforme o parâmetro.
func (r *ConversationRepo) Create(ctx context.Context, tx pgx.Tx, tenantID TenantID, contactID ContactID, channelIdentityID ChannelIdentityID, channel string, botEnabled bool) (*Conversation, error) {
	var c Conversation
	var modality *string
	var state, channelOut string

	err := tx.QueryRow(ctx, `
		INSERT INTO conversation (tenant_id, channel_identity_id, channel, state, bot_enabled)
		VALUES ($1, $2, $3::channel, 'NEW', $4)
		RETURNING id, tenant_id, channel_identity_id, channel::text, state::text, bot_enabled,
		          last_inbound_at, last_outbound_at, last_inbound_modality,
		          created_at, updated_at
	`, tenantID, channelIdentityID, channel, botEnabled).Scan(
		&c.ID, &c.TenantID, &c.ChannelIdentityID, &channelOut,
		&state, &c.BotEnabled,
		&c.LastInboundAt, &c.LastOutboundAt, &modality,
		&c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("ConversationRepo.Create: %w", err)
	}
	c.ContactID = contactID
	c.State = ConversationState(state)
	c.Channel = channelOut
	if modality != nil {
		c.LastInboundModality = *modality
	}
	return &c, nil
}

// Save persiste as colunas mutáveis de uma conversa (state, timestamps, etc.).
func (r *ConversationRepo) Save(ctx context.Context, tx pgx.Tx, conv Conversation) error {
	_, err := tx.Exec(ctx, `
		UPDATE conversation
		SET state = $1::conversation_state,
		    last_inbound_at = $2,
		    last_outbound_at = $3,
		    last_inbound_modality = $4::reply_modality,
		    updated_at = now()
		WHERE tenant_id = $5 AND id = $6
	`, string(conv.State), conv.LastInboundAt, conv.LastOutboundAt,
		nullableString(conv.LastInboundModality),
		conv.TenantID, conv.ID)
	if err != nil {
		return fmt.Errorf("ConversationRepo.Save: %w", err)
	}
	return nil
}

// SaveReactivation persiste uma reativação pedida pelo cliente via o engine.
func (r *ConversationRepo) SaveReactivation(ctx context.Context, tx pgx.Tx, convID ConversationID, tenantID TenantID, contactID ContactID, sc *ScheduledContact) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO scheduled_contacts (tenant_id, contact_id, conversation_id, fire_at, status, payload)
		VALUES ($1, $2, $3, $4::date, 'pending', '{"kind":"reactivation"}'::jsonb)
		ON CONFLICT DO NOTHING
	`, tenantID, contactID, convID, sc.ResolvedDate)
	if err != nil {
		return fmt.Errorf("ConversationRepo.SaveReactivation: %w", err)
	}
	return nil
}

// SetBotEnabled liga ou desliga o bot para uma conversa.
func (r *ConversationRepo) SetBotEnabled(ctx context.Context, tx pgx.Tx, tenantID TenantID, convID ConversationID, enabled bool) error {
	_, err := tx.Exec(ctx, `
		UPDATE conversation SET bot_enabled = $1, updated_at = now()
		WHERE tenant_id = $2 AND id = $3
	`, enabled, tenantID, convID)
	if err != nil {
		return fmt.Errorf("ConversationRepo.SetBotEnabled: %w", err)
	}
	return nil
}

// SetState atualiza apenas o estado do FSM de uma conversa.
func (r *ConversationRepo) SetState(ctx context.Context, tx pgx.Tx, tenantID TenantID, convID ConversationID, state ConversationState) error {
	_, err := tx.Exec(ctx, `
		UPDATE conversation SET state = $1::conversation_state, updated_at = now()
		WHERE tenant_id = $2 AND id = $3
	`, string(state), tenantID, convID)
	if err != nil {
		return fmt.Errorf("ConversationRepo.SetState: %w", err)
	}
	return nil
}

// ============================================================================
// MessageRepo — gravação de inbound + histórico de turnos
// ============================================================================

type MessageRepo struct{ pool *pgxpool.Pool }

// RecordInbound grava uma mensagem inbound. Retorna true se foi inserida pela
// primeira vez (false = duplicata — wamid já existe).
// Assinatura alinhada com engine.go: (ctx, tx, wamid, tenantID, convID, content)
func (r *MessageRepo) RecordInbound(ctx context.Context, tx pgx.Tx, wamid string, tenantID TenantID, convID ConversationID, content MessageContent) (bool, error) {
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return false, fmt.Errorf("MessageRepo.RecordInbound marshal: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO inbound_message (tenant_id, conversation_id, provider_message_id, content, received_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (tenant_id, provider_message_id) DO NOTHING
	`, tenantID, convID, wamid, contentJSON)
	if err != nil {
		return false, fmt.Errorf("MessageRepo.RecordInbound: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RecordOutbound persiste um balão de saída em outbound_message.
// reasoning é o JSON do ResponderOutput, armazenado apenas no primeiro balão (nil para os demais).
func (r *MessageRepo) RecordOutbound(ctx context.Context, tx pgx.Tx, idempotencyKey string, tenantID TenantID, convID ConversationID, providerMessageID string, text string, reasoning *string) error {
	content := MessageContent{Type: "text", Text: text}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("MessageRepo.RecordOutbound marshal: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO outbound_message
		  (tenant_id, conversation_id, provider_message_id, idempotency_key, intent_category, content, status, sent_at, reasoning)
		VALUES ($1, $2, $3, $4, 'FREE_FORM'::outbound_intent, $5, 'sent', now(), $6)
		ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
	`, tenantID, convID, nullableString(providerMessageID), idempotencyKey, contentJSON, reasoning)
	if err != nil {
		return fmt.Errorf("MessageRepo.RecordOutbound: %w", err)
	}
	return nil
}

// SetNextRetryAt agenda o próximo retry de uma mensagem inbound (quiet hours).
// Marca o webhook event correspondente — não há coluna em inbound_message para isso;
// guardamos a informação no webhook_events pelo provider_message_id.
func (r *MessageRepo) SetNextRetryAt(ctx context.Context, tx pgx.Tx, wamid string, nextRetry time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE webhook_events
		SET next_retry_at = $1, processing_status = 'failed'
		WHERE provider_event_id = $2
	`, nextRetry, wamid)
	if err != nil {
		return fmt.Errorf("MessageRepo.SetNextRetryAt: %w", err)
	}
	return nil
}

// GetRecentTurns retorna os últimos turnos da conversa em ordem cronológica.
// Assinatura alinhada com engine.go: (ctx, tx, convID) — limit fixo em 20.
func (r *MessageRepo) GetRecentTurns(ctx context.Context, tx pgx.Tx, convID ConversationID) ([]ConversationTurn, error) {
	const limit = 20
	rows, err := tx.Query(ctx, `
		SELECT role, text FROM (
			SELECT 'user'                                    AS role,
			       COALESCE(content->>'Text', '[mídia]')     AS text,
			       received_at                               AS ts
			FROM inbound_message
			WHERE conversation_id = $1

			UNION ALL

			SELECT 'assistant'         AS role,
			       content->>'Text'    AS text,
			       created_at          AS ts
			FROM outbound_message
			WHERE conversation_id = $1
			  AND content->>'Text' IS NOT NULL
		) sub
		ORDER BY ts DESC
		LIMIT $2
	`, convID, limit)
	if err != nil {
		return nil, fmt.Errorf("MessageRepo.GetRecentTurns: %w", err)
	}
	defer rows.Close()

	var turns []ConversationTurn
	for rows.Next() {
		var t ConversationTurn
		if err := rows.Scan(&t.Role, &t.Text); err != nil {
			return nil, err
		}
		turns = append(turns, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Inverte para ordem cronológica (mais antigo primeiro).
	for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
		turns[i], turns[j] = turns[j], turns[i]
	}
	return turns, nil
}

// ============================================================================
// LeadRepo
// ============================================================================

type LeadRepo struct{ pool *pgxpool.Pool }

// FindByConversation encontra o lead vinculado a uma conversa via contact_id.
func (r *LeadRepo) FindByConversation(ctx context.Context, tenantID TenantID, convID ConversationID) (*Lead, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT l.id, l.tenant_id, l.contact_id, $2::uuid AS conversation_id, l.status, l.created_at
		FROM lead l
		JOIN conversation c ON c.contact_id = l.contact_id AND c.tenant_id = l.tenant_id
		WHERE l.tenant_id = $1 AND c.id = $2
	`, tenantID, convID)

	var l Lead
	err := row.Scan(&l.ID, &l.TenantID, &l.ContactID, &l.ConversationID, &l.Status, &l.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("LeadRepo.FindByConversation: %w", err)
	}
	return &l, nil
}

// Create cria um lead com status='novo' (CRM). ON CONFLICT mantém o status atual
// (idempotente — não rebaixa um lead que já avançou no funil).
func (r *LeadRepo) Create(ctx context.Context, tx pgx.Tx, tenantID TenantID, contactID ContactID, convID ConversationID) (*Lead, error) {
	var l Lead
	l.TenantID = tenantID
	l.ContactID = contactID
	l.ConversationID = convID
	l.Status = "novo"

	err := tx.QueryRow(ctx, `
		INSERT INTO lead (tenant_id, contact_id, status)
		VALUES ($1, $2, 'novo')
		ON CONFLICT (tenant_id, contact_id) DO UPDATE SET status = lead.status
		RETURNING id, created_at
	`, tenantID, contactID).Scan(&l.ID, &l.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("LeadRepo.Create: %w", err)
	}
	return &l, nil
}

// SetStatusByConversation avança o status do lead vinculado a uma conversa (via
// contact). Não rebaixa leads já em 'matriculado'/'perdido'. Usado pelo tie-in
// de agendamento (confirmar aula → 'aula_marcada').
func (r *LeadRepo) SetStatusByConversation(ctx context.Context, tx pgx.Tx, tenantID TenantID, convID ConversationID, status string) error {
	_, err := tx.Exec(ctx, `
		UPDATE lead l
		SET status = $3, updated_at = now()
		FROM conversation c
		WHERE c.id = $2 AND c.tenant_id = $1
		  AND l.tenant_id = $1 AND l.contact_id = c.contact_id
		  AND l.status NOT IN ('matriculado', 'perdido')
	`, tenantID, convID, status)
	if err != nil {
		return fmt.Errorf("LeadRepo.SetStatusByConversation: %w", err)
	}
	return nil
}

// ============================================================================
// OutboxRepo — domain_events outbox + outbound_message exactly-once
// ============================================================================

type OutboxRepo struct{ pool *pgxpool.Pool }

// Emit grava um DomainEvent no outbox dentro de uma transação existente.
// Implementa a interface EventEmitter do engine.
func (r *OutboxRepo) Emit(ctx context.Context, tx pgx.Tx, event DomainEvent) error {
	payloadJSON, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("OutboxRepo.Emit marshal payload: %w", err)
	}

	occurredAt := event.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO domain_events (tenant_id, event_type, aggregate_id, payload, occurred_at)
		VALUES ($1, $2, $3, $4, $5)
	`, event.TenantID, event.Type, event.AggregateID, payloadJSON, occurredAt)
	if err != nil {
		return fmt.Errorf("OutboxRepo.Emit: %w", err)
	}
	return nil
}

// Drain retorna até batchSize eventos não processados, travando com SKIP LOCKED.
func (r *OutboxRepo) Drain(ctx context.Context, batchSize int) ([]DomainEvent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, aggregate_id, event_type, payload, occurred_at
		FROM domain_events
		WHERE processed_at IS NULL
		ORDER BY occurred_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, batchSize)
	if err != nil {
		return nil, fmt.Errorf("OutboxRepo.Drain: %w", err)
	}
	defer rows.Close()

	var events []DomainEvent
	for rows.Next() {
		var e DomainEvent
		var payloadRaw []byte
		if err := rows.Scan(&e.ID, &e.TenantID, &e.AggregateID, &e.Type, &payloadRaw, &e.OccurredAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payloadRaw, &e.Payload); err != nil {
			return nil, fmt.Errorf("OutboxRepo.Drain unmarshal payload: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// MarkProcessed marca um evento do outbox como processado.
func (r *OutboxRepo) MarkProcessed(ctx context.Context, id DomainEventID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE domain_events SET processed_at = now() WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("OutboxRepo.MarkProcessed: %w", err)
	}
	return nil
}

// MarkFailed registra falha no evento do outbox.
func (r *OutboxRepo) MarkFailed(ctx context.Context, id DomainEventID, errMsg string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE domain_events
		SET attempts = attempts + 1, last_error = $1
		WHERE id = $2
	`, errMsg, id)
	if err != nil {
		return fmt.Errorf("OutboxRepo.MarkFailed: %w", err)
	}
	return nil
}

// InsertOutbound grava um OutboundMessage em outbound_message dentro de uma tx.
// Retorna true se foi inserida (false = chave idempotente já existia).
func (r *OutboxRepo) InsertOutbound(ctx context.Context, tx pgx.Tx, msg OutboundMessage) (bool, error) {
	contentJSON, err := json.Marshal(msg.Content)
	if err != nil {
		return false, fmt.Errorf("OutboxRepo.InsertOutbound marshal content: %w", err)
	}

	var templateJSON []byte
	if msg.TemplatePayload != nil {
		templateJSON, err = json.Marshal(msg.TemplatePayload)
		if err != nil {
			return false, fmt.Errorf("OutboxRepo.InsertOutbound marshal template: %w", err)
		}
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO outbound_message (tenant_id, conversation_id, idempotency_key, intent_category, content, template_payload)
		VALUES ($1, $2, $3, $4::outbound_intent, $5, $6)
		ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
	`, msg.TenantID, msg.ConversationID, msg.IdempotencyKey, string(msg.Intent), contentJSON, templateJSON)
	if err != nil {
		return false, fmt.Errorf("OutboxRepo.InsertOutbound: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ClaimOutbound tenta marcar outbound_message como 'sent' de forma exactly-once.
// Retorna true se a linha foi atualizada (false = já estava sent ou não existe).
func (r *OutboxRepo) ClaimOutbound(ctx context.Context, tenantID TenantID, idempotencyKey string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE outbound_message
		SET status = 'sent', sent_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND idempotency_key = $2 AND status = 'pending'
	`, tenantID, idempotencyKey)
	if err != nil {
		return false, fmt.Errorf("OutboxRepo.ClaimOutbound: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ============================================================================
// WebhookRepo
// ============================================================================

type WebhookRepo struct{ pool *pgxpool.Pool }

// Record grava um webhook_event. Retorna (id, isDuplicate).
// isDuplicate=true quando (tenant_id, provider, provider_event_id) já existia.
func (r *WebhookRepo) Record(ctx context.Context, tenantID TenantID, provider, providerEventID string, raw []byte) (WebhookEventID, bool, error) {
	rawPayload := json.RawMessage(raw)
	if !json.Valid(raw) {
		rawPayload = json.RawMessage(`{}`)
	}

	var id WebhookEventID
	err := r.pool.QueryRow(ctx, `
		INSERT INTO webhook_events (tenant_id, provider, provider_event_id, raw_payload, raw_body)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, provider, provider_event_id) DO NOTHING
		RETURNING id
	`, tenantID, provider, providerEventID, rawPayload, raw).Scan(&id)

	if err == pgx.ErrNoRows {
		// Conflito — evento duplicado. Busca o id existente.
		err2 := r.pool.QueryRow(ctx, `
			SELECT id FROM webhook_events
			WHERE tenant_id = $1 AND provider = $2 AND provider_event_id = $3
		`, tenantID, provider, providerEventID).Scan(&id)
		if err2 != nil {
			return "", true, fmt.Errorf("WebhookRepo.Record lookup duplicate: %w", err2)
		}
		return id, true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("WebhookRepo.Record: %w", err)
	}
	return id, false, nil
}

// MarkDone marca o webhook como processado com sucesso.
func (r *WebhookRepo) MarkDone(ctx context.Context, id WebhookEventID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE webhook_events
		SET processing_status = 'done', processed_at = now()
		WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("WebhookRepo.MarkDone: %w", err)
	}
	return nil
}

// MarkFailed marca o webhook como falho e agenda o próximo retry com backoff exponencial.
func (r *WebhookRepo) MarkFailed(ctx context.Context, id WebhookEventID, errMsg string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE webhook_events
		SET processing_status = 'failed',
		    last_error  = left($1, 500),
		    attempts    = attempts + 1,
		    next_retry_at = now() + (power(2, attempts) * interval '1 minute')
		WHERE id = $2
	`, errMsg, id)
	if err != nil {
		return fmt.Errorf("WebhookRepo.MarkFailed: %w", err)
	}
	return nil
}

// PendingRetries retorna webhooks com status 'failed' e next_retry_at vencido.
func (r *WebhookRepo) PendingRetries(ctx context.Context, limit int) ([]WebhookEvent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, provider, provider_event_id, raw_payload,
		       processing_status, attempts, next_retry_at, last_error,
		       processed_at, received_at
		FROM webhook_events
		WHERE processing_status = 'failed'
		  AND (next_retry_at IS NULL OR next_retry_at <= now())
		ORDER BY next_retry_at NULLS FIRST
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("WebhookRepo.PendingRetries: %w", err)
	}
	defer rows.Close()

	var events []WebhookEvent
	for rows.Next() {
		var e WebhookEvent
		var rawPayload []byte
		var status string
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.Provider, &e.ProviderEventID, &rawPayload,
			&status, &e.Attempts, &e.NextRetryAt, &e.LastError,
			&e.ProcessedAt, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		e.RawPayload = rawPayload
		e.Status = status
		events = append(events, e)
	}
	return events, rows.Err()
}

// ============================================================================
// TenantConfigRepo
// ============================================================================

type TenantConfigRepo struct{ pool *pgxpool.Pool }

// Get retorna a configuração do tenant. Aceita tx para uso dentro de withTenant.
// Assinatura alinhada com engine.go: (ctx, tx, tenantID).
func (r *TenantConfigRepo) Get(ctx context.Context, tx pgx.Tx, tenantID TenantID) (*TenantConfig, error) {
	const query = `
		SELECT tc.bot_name, tc.bot_gender,
		       false AS reveal_ai_if_asked,
		       tc.kb_content::text,
		       tc.system_prompt,
		       tc.admin_system_prompt,
		       t.timezone,
		       tc.quiet_hours->>'start' AS quiet_hours_start,
		       tc.quiet_hours->>'end'   AS quiet_hours_end,
		       tc.bot_enabled_by_default,
		       tc.bot_allowed_numbers,
		       tc.admin_whatsapp_number,
		       tc.admin_whatsapp_numbers
		FROM tenant_config tc
		JOIN tenants t ON t.id = tc.tenant_id
		WHERE tc.tenant_id = $1
	`

	// tx é opcional: quando nil (ex.: chamadas do worker fora de transação),
	// usa o pool diretamente. Chamar tx.QueryRow num pgx.Tx nil causaria panic.
	var row pgx.Row
	if tx != nil {
		row = tx.QueryRow(ctx, query, tenantID)
	} else {
		row = r.pool.QueryRow(ctx, query, tenantID)
	}

	var cfg TenantConfig
	cfg.TenantID = tenantID

	var kbJSON *string
	var quietStart, quietEnd *string
	var allowedRaw []byte
	var adminNumbersRaw []byte

	err := row.Scan(
		&cfg.BotName, &cfg.BotGender,
		&cfg.RevealAIIfAsked,
		&kbJSON,
		&cfg.SystemPrompt,
		&cfg.AdminSystemPrompt,
		&cfg.Timezone,
		&quietStart, &quietEnd,
		&cfg.BotEnabledByDefault,
		&allowedRaw,
		&cfg.AdminWhatsAppNumber,
		&adminNumbersRaw,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("TenantConfigRepo.Get: tenant %s não encontrado", tenantID)
	}
	if err != nil {
		return nil, fmt.Errorf("TenantConfigRepo.Get: %w", err)
	}

	cfg.KBContent = kbJSON
	cfg.QuietHoursStart = quietStart
	cfg.QuietHoursEnd = quietEnd
	if len(allowedRaw) > 0 {
		_ = json.Unmarshal(allowedRaw, &cfg.BotAllowedNumbers)
	}
	if len(adminNumbersRaw) > 0 {
		_ = json.Unmarshal(adminNumbersRaw, &cfg.AdminWhatsAppNumbers)
	}

	return &cfg, nil
}

// AppendKBEntry adiciona uma entrada à base de conhecimento do tenant via jsonb append.
func (r *TenantConfigRepo) AppendKBEntry(ctx context.Context, tenantID TenantID, entry KBEntry) error {
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("AppendKBEntry: marshal: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		UPDATE tenant_config
		SET kb_content = COALESCE(kb_content, '[]'::jsonb) || $2::jsonb
		WHERE tenant_id = $1
	`, tenantID, "["+string(entryJSON)+"]")
	if err != nil {
		return fmt.Errorf("AppendKBEntry: exec: %w", err)
	}
	return nil
}

// ============================================================================
// PendingQuestionRepo — fila de dúvidas de clientes aguardando o admin (0015)
// ============================================================================

type PendingQuestionRepo struct{ pool *pgxpool.Pool }

func NewPendingQuestionRepo(pool *pgxpool.Pool) *PendingQuestionRepo {
	return &PendingQuestionRepo{pool: pool}
}

// Insert grava uma dúvida pendente quando o bot dá handoff. Idempotente por
// conversa+pergunta abertas para não duplicar se o cliente reenviar.
func (r *PendingQuestionRepo) Insert(ctx context.Context, tx pgx.Tx, tenantID TenantID, convID ConversationID, clientPhone, clientName, question string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO pending_question (tenant_id, conversation_id, client_phone, client_name, question, status)
		SELECT $1, $2, $3, $4, $5, 'open'
		WHERE NOT EXISTS (
			SELECT 1 FROM pending_question
			WHERE tenant_id = $1 AND conversation_id = $2 AND status IN ('open', 'drafted')
		)
	`, tenantID, convID, clientPhone, clientName, question)
	if err != nil {
		return fmt.Errorf("PendingQuestionRepo.Insert: %w", err)
	}
	return nil
}

// ListOpen retorna as pendências abertas/rascunhadas do tenant (para casamento).
func (r *PendingQuestionRepo) ListOpen(ctx context.Context, tx pgx.Tx, tenantID TenantID) ([]PendingQuestion, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, conversation_id, client_phone, client_name, question, status, COALESCE(draft, ''), created_at
		FROM pending_question
		WHERE tenant_id = $1 AND status IN ('open', 'drafted')
		ORDER BY created_at
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("PendingQuestionRepo.ListOpen: %w", err)
	}
	defer rows.Close()

	var out []PendingQuestion
	for rows.Next() {
		pq := PendingQuestion{TenantID: tenantID}
		if err := rows.Scan(&pq.ID, &pq.ConversationID, &pq.ClientPhone, &pq.ClientName,
			&pq.Question, &pq.Status, &pq.Draft, &pq.CreatedAt); err != nil {
			return nil, fmt.Errorf("PendingQuestionRepo.ListOpen scan: %w", err)
		}
		out = append(out, pq)
	}
	return out, rows.Err()
}

// Get carrega uma pendência por id (escopada ao tenant). Retorna nil se ausente
// ou já resolvida — usado como guarda de idempotência antes de enviar ao cliente.
func (r *PendingQuestionRepo) Get(ctx context.Context, tx pgx.Tx, tenantID TenantID, id string) (*PendingQuestion, error) {
	pq := PendingQuestion{TenantID: tenantID, ID: id}
	err := tx.QueryRow(ctx, `
		SELECT conversation_id, client_phone, client_name, question, status, COALESCE(draft, ''), created_at
		FROM pending_question
		WHERE tenant_id = $1 AND id = $2 AND status IN ('open', 'drafted')
	`, tenantID, id).Scan(&pq.ConversationID, &pq.ClientPhone, &pq.ClientName,
		&pq.Question, &pq.Status, &pq.Draft, &pq.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("PendingQuestionRepo.Get: %w", err)
	}
	return &pq, nil
}

// StoreDraft grava o rascunho proposto e move a pendência para 'drafted'.
func (r *PendingQuestionRepo) StoreDraft(ctx context.Context, tx pgx.Tx, tenantID TenantID, id, draft string) error {
	_, err := tx.Exec(ctx, `
		UPDATE pending_question SET draft = $3, status = 'drafted', updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND status IN ('open', 'drafted')
	`, tenantID, id, draft)
	if err != nil {
		return fmt.Errorf("PendingQuestionRepo.StoreDraft: %w", err)
	}
	return nil
}

// MarkResolved marca a pendência como resolvida (após enviar ao cliente).
func (r *PendingQuestionRepo) MarkResolved(ctx context.Context, tx pgx.Tx, tenantID TenantID, id string) error {
	_, err := tx.Exec(ctx, `
		UPDATE pending_question SET status = 'resolved', updated_at = now()
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, id)
	if err != nil {
		return fmt.Errorf("PendingQuestionRepo.MarkResolved: %w", err)
	}
	return nil
}

// ============================================================================
// PendingBookingRepo — agendamentos aguardando confirmação do admin (0018)
// ============================================================================

type PendingBookingRepo struct{ pool *pgxpool.Pool }

func NewPendingBookingRepo(pool *pgxpool.Pool) *PendingBookingRepo {
	return &PendingBookingRepo{pool: pool}
}

// Insert grava um agendamento proposto (status 'open').
func (r *PendingBookingRepo) Insert(ctx context.Context, tx pgx.Tx, b PendingBooking) error {
	var agePtr *int
	if b.Age > 0 {
		agePtr = &b.Age
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO pending_booking
		  (tenant_id, conversation_id, client_phone, client_name, kind, course,
		   proposed_day, proposed_time, proposed_period, age, notes, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'open')
	`, b.TenantID, b.ConversationID, b.ClientPhone, b.ClientName, b.Kind, b.Course,
		b.ProposedDay, b.ProposedTime, b.ProposedPeriod, agePtr, b.Notes)
	if err != nil {
		return fmt.Errorf("PendingBookingRepo.Insert: %w", err)
	}
	return nil
}

// ListOpen retorna os agendamentos abertos do tenant (para o prompt do admin).
func (r *PendingBookingRepo) ListOpen(ctx context.Context, tx pgx.Tx, tenantID TenantID) ([]PendingBooking, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, conversation_id, client_phone, client_name, kind, course,
		       proposed_day, proposed_time, proposed_period, COALESCE(age, 0), notes, status, created_at
		FROM pending_booking
		WHERE tenant_id = $1 AND status = 'open'
		ORDER BY created_at
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("PendingBookingRepo.ListOpen: %w", err)
	}
	defer rows.Close()

	var out []PendingBooking
	for rows.Next() {
		b := PendingBooking{TenantID: tenantID}
		if err := rows.Scan(&b.ID, &b.ConversationID, &b.ClientPhone, &b.ClientName, &b.Kind, &b.Course,
			&b.ProposedDay, &b.ProposedTime, &b.ProposedPeriod, &b.Age, &b.Notes, &b.Status, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("PendingBookingRepo.ListOpen scan: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Get carrega um agendamento aberto por id (guarda de idempotência).
func (r *PendingBookingRepo) Get(ctx context.Context, tx pgx.Tx, tenantID TenantID, id string) (*PendingBooking, error) {
	b := PendingBooking{TenantID: tenantID, ID: id}
	err := tx.QueryRow(ctx, `
		SELECT conversation_id, client_phone, client_name, kind, course,
		       proposed_day, proposed_time, proposed_period, COALESCE(age, 0), notes, status, created_at
		FROM pending_booking
		WHERE tenant_id = $1 AND id = $2 AND status = 'open'
	`, tenantID, id).Scan(&b.ConversationID, &b.ClientPhone, &b.ClientName, &b.Kind, &b.Course,
		&b.ProposedDay, &b.ProposedTime, &b.ProposedPeriod, &b.Age, &b.Notes, &b.Status, &b.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("PendingBookingRepo.Get: %w", err)
	}
	return &b, nil
}

// MarkStatus atualiza o status (confirmed | rejected) de um agendamento.
func (r *PendingBookingRepo) MarkStatus(ctx context.Context, tx pgx.Tx, tenantID TenantID, id, status string) error {
	_, err := tx.Exec(ctx, `
		UPDATE pending_booking SET status = $3, updated_at = now()
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, id, status)
	if err != nil {
		return fmt.Errorf("PendingBookingRepo.MarkStatus: %w", err)
	}
	return nil
}

// ============================================================================
// ScheduledContactRepo
// ============================================================================

type ScheduledContactRepo struct{ pool *pgxpool.Pool }

// ScheduleReactivation agenda uma reativação para a data indicada (YYYY-MM-DD).
func (r *ScheduledContactRepo) ScheduleReactivation(ctx context.Context, tx pgx.Tx, tenantID TenantID, contactID ContactID, convID ConversationID, date string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO scheduled_contacts (tenant_id, contact_id, conversation_id, fire_at, status, payload)
		VALUES ($1, $2, $3, $4::date, 'pending', '{"kind":"reactivation"}'::jsonb)
		ON CONFLICT DO NOTHING
	`, tenantID, contactID, convID, date)
	if err != nil {
		return fmt.Errorf("ScheduledContactRepo.ScheduleReactivation: %w", err)
	}
	return nil
}

// PendingFollowUps retorna follow-ups vencidos com status pending, FOR UPDATE SKIP LOCKED.
func (r *ScheduledContactRepo) PendingFollowUps(ctx context.Context, limit int) ([]ScheduledContactRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, contact_id, conversation_id,
		       payload->>'kind'     AS kind,
		       status, fire_at, attempts, payload,
		       created_at
		FROM scheduled_contacts
		WHERE payload->>'kind' = 'follow_up'
		  AND status = 'pending'
		  AND fire_at <= now()
		ORDER BY fire_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("ScheduledContactRepo.PendingFollowUps: %w", err)
	}
	defer rows.Close()

	return scanScheduledRows(rows)
}

// MarkFollowUpSent marca um follow-up como disparado com sucesso.
func (r *ScheduledContactRepo) MarkFollowUpSent(ctx context.Context, id ScheduledContactID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE scheduled_contacts
		SET status = 'fired', sent_at = now(), updated_at = now()
		WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("ScheduledContactRepo.MarkFollowUpSent: %w", err)
	}
	return nil
}

// MarkFollowUpFailed registra falha no follow-up.
func (r *ScheduledContactRepo) MarkFollowUpFailed(ctx context.Context, id ScheduledContactID, errMsg string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE scheduled_contacts
		SET status = 'failed', last_error = $1, attempts = attempts + 1,
		    last_attempted_at = now(), updated_at = now()
		WHERE id = $2
	`, errMsg, id)
	if err != nil {
		return fmt.Errorf("ScheduledContactRepo.MarkFollowUpFailed: %w", err)
	}
	return nil
}

// ScheduleFollowUp agenda um follow-up para scheduledAt.
// Sem tx — chamado pelo worker sem transação aberta.
// Idempotente via índice parcial único (ux_scheduled_contacts_active_follow_up).
func (r *ScheduledContactRepo) ScheduleFollowUp(ctx context.Context, tenantID TenantID, contactID ContactID, convID ConversationID, scheduledAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO scheduled_contacts (tenant_id, contact_id, conversation_id, fire_at, status, payload)
		VALUES ($1, $2, $3, $4, 'pending', '{"kind":"follow_up"}'::jsonb)
		ON CONFLICT DO NOTHING
	`, tenantID, contactID, convID, scheduledAt)
	if err != nil {
		return fmt.Errorf("ScheduledContactRepo.ScheduleFollowUp: %w", err)
	}
	return nil
}

// CancelFollowUps marca como 'cancelled' todos os follow-ups pendentes da conversa.
// Sem tx — chamado pelo worker sem transação aberta.
func (r *ScheduledContactRepo) CancelFollowUps(ctx context.Context, tenantID TenantID, convID ConversationID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE scheduled_contacts
		SET status = 'cancelled', updated_at = now()
		WHERE tenant_id = $1
		  AND conversation_id = $2
		  AND status = 'pending'
		  AND payload->>'kind' = 'follow_up'
	`, tenantID, convID)
	if err != nil {
		return fmt.Errorf("ScheduledContactRepo.CancelFollowUps: %w", err)
	}
	return nil
}

// ============================================================================
// Helpers internos
// ============================================================================

func scanScheduledRows(rows pgx.Rows) ([]ScheduledContactRow, error) {
	var results []ScheduledContactRow
	for rows.Next() {
		var r ScheduledContactRow
		var payloadRaw []byte
		if err := rows.Scan(
			&r.ID, &r.TenantID, &r.ContactID, &r.ConversationID,
			&r.Kind, &r.Status, &r.ScheduledAt, &r.Attempts, &payloadRaw,
			&r.CreatedAt,
		); err != nil {
			return nil, err
		}
		var payload struct {
			TemplateVars map[string]string `json:"templateVars"`
		}
		if len(payloadRaw) > 0 {
			_ = json.Unmarshal(payloadRaw, &payload)
		}
		r.TemplateVars = payload.TemplateVars
		results = append(results, r)
	}
	return results, rows.Err()
}

// nullableString converte string vazia em nil para colunas nullable.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
