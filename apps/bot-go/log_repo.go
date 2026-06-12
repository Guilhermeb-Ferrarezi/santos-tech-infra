package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ProcessingLogEntry representa um registro de processamento de mensagem.
type ProcessingLogEntry struct {
	ID             string
	TenantID       string
	ConversationID string
	ContactPhone   string
	ContactName    string
	InboundText    string
	Answered       *bool
	AnsweredFromKb *bool
	Handoff        *bool
	CitedEntryIDs  []string
	Bubbles        []string
	ProcessingMs   int
	Error          string
	CreatedAt      time.Time
}

// ProcessingLogRepo persiste e consulta logs de processamento.
type ProcessingLogRepo struct {
	pool *pgxpool.Pool
}

func NewProcessingLogRepo(pool *pgxpool.Pool) *ProcessingLogRepo {
	return &ProcessingLogRepo{pool: pool}
}

// Insert grava um log entry. Não falha silenciosamente — retorna erro para o chamador logar.
func (r *ProcessingLogRepo) Insert(ctx context.Context, e ProcessingLogEntry) error {
	citedJSON, _ := json.Marshal(e.CitedEntryIDs)
	bubblesJSON, _ := json.Marshal(e.Bubbles)
	convID := nullableString(e.ConversationID)

	_, err := r.pool.Exec(ctx, `
		INSERT INTO bot_processing_log
		  (tenant_id, conversation_id, contact_phone, contact_name, inbound_text,
		   answered, answered_from_kb, handoff, cited_entry_ids, bubbles, processing_ms, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, e.TenantID, convID, e.ContactPhone, e.ContactName, e.InboundText,
		e.Answered, e.AnsweredFromKb, e.Handoff,
		citedJSON, bubblesJSON, e.ProcessingMs, nullableString(e.Error))
	if err != nil {
		return fmt.Errorf("ProcessingLogRepo.Insert: %w", err)
	}
	return nil
}

// List retorna os últimos logs de um tenant, paginados por cursor (created_at).
// Se cursor for zero, retorna os mais recentes.
func (r *ProcessingLogRepo) List(ctx context.Context, tenantID string, limit int, before time.Time) ([]ProcessingLogEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	cursor := before
	if cursor.IsZero() {
		cursor = time.Now().Add(time.Second) // ligeiramente no futuro para incluir todos
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id::text, tenant_id,
		       COALESCE(conversation_id::text, ''),
		       contact_phone, contact_name, inbound_text,
		       answered, answered_from_kb, handoff,
		       cited_entry_ids::text, bubbles::text,
		       processing_ms, COALESCE(error, ''), created_at
		FROM bot_processing_log
		WHERE tenant_id = $1 AND created_at < $2
		ORDER BY created_at DESC
		LIMIT $3
	`, tenantID, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("ProcessingLogRepo.List: %w", err)
	}
	defer rows.Close()

	var entries []ProcessingLogEntry
	for rows.Next() {
		var e ProcessingLogEntry
		var citedRaw, bubblesRaw string
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.ConversationID,
			&e.ContactPhone, &e.ContactName, &e.InboundText,
			&e.Answered, &e.AnsweredFromKb, &e.Handoff,
			&citedRaw, &bubblesRaw,
			&e.ProcessingMs, &e.Error, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("ProcessingLogRepo.List scan: %w", err)
		}
		_ = json.Unmarshal([]byte(citedRaw), &e.CitedEntryIDs)
		_ = json.Unmarshal([]byte(bubblesRaw), &e.Bubbles)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
