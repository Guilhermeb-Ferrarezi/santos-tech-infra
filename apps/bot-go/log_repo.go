package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ProcessingLogEntry representa um registro de processamento de mensagem ou uma
// AÇÃO do sistema (Kind="action", ex.: agendamento gravado no Notion) que não está
// amarrada a um turno de conversa.
type ProcessingLogEntry struct {
	ID             string
	Kind           string // "message" (padrão) | "action"
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
	ToolCalls      json.RawMessage
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

	var toolCallsArg any
	if len(e.ToolCalls) > 0 {
		toolCallsArg = e.ToolCalls
	}

	kind := e.Kind
	if kind == "" {
		kind = "message"
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO bot_processing_log
		  (tenant_id, kind, conversation_id, contact_phone, contact_name, inbound_text,
		   answered, answered_from_kb, handoff, cited_entry_ids, bubbles, tool_calls, processing_ms, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, e.TenantID, kind, convID, e.ContactPhone, e.ContactName, e.InboundText,
		e.Answered, e.AnsweredFromKb, e.Handoff,
		citedJSON, bubblesJSON, toolCallsArg, e.ProcessingMs, nullableString(e.Error))
	if err != nil {
		return fmt.Errorf("ProcessingLogRepo.Insert: %w", err)
	}
	return nil
}

// LogFilters — filtros opcionais para a listagem paginada de logs.
type LogFilters struct {
	Answered       *bool  // respondeu (sim/não)
	AnsweredFromKb *bool  // usou a KB
	Handoff        *bool  // encaminhou para humano
	ErrorsOnly     bool   // apenas entradas com erro
	Search         string // busca em contato/telefone/texto da mensagem
}

// ListPaged retorna uma página de logs (ordem desc por created_at) com filtros
// e o total de registros que casam (para o cálculo de páginas no dashboard).
func (r *ProcessingLogRepo) ListPaged(ctx context.Context, tenantID string, f LogFilters, page, pageSize int) ([]ProcessingLogEntry, int, error) {
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	if page < 1 {
		page = 1
	}

	conds := []string{"tenant_id = $1"}
	args := []any{tenantID}
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Answered != nil {
		add("answered = $%d", *f.Answered)
	}
	if f.AnsweredFromKb != nil {
		add("answered_from_kb = $%d", *f.AnsweredFromKb)
	}
	if f.Handoff != nil {
		add("handoff = $%d", *f.Handoff)
	}
	if f.ErrorsOnly {
		conds = append(conds, "error IS NOT NULL AND error <> ''")
	}
	if f.Search != "" {
		args = append(args, "%"+f.Search+"%")
		i := len(args)
		conds = append(conds, fmt.Sprintf("(contact_name ILIKE $%d OR contact_phone ILIKE $%d OR inbound_text ILIKE $%d)", i, i, i))
	}
	where := strings.Join(conds, " AND ")

	var total int
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM bot_processing_log WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("ProcessingLogRepo.ListPaged count: %w", err)
	}

	args = append(args, pageSize, (page-1)*pageSize)
	query := fmt.Sprintf(`
		SELECT id::text, COALESCE(kind, 'message'), tenant_id,
		       COALESCE(conversation_id::text, ''),
		       contact_phone, contact_name, inbound_text,
		       answered, answered_from_kb, handoff,
		       cited_entry_ids::text, bubbles::text,
		       COALESCE(tool_calls::text, 'null'),
		       processing_ms, COALESCE(error, ''), created_at
		FROM bot_processing_log
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, len(args)-1, len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("ProcessingLogRepo.ListPaged: %w", err)
	}
	defer rows.Close()

	var entries []ProcessingLogEntry
	for rows.Next() {
		var e ProcessingLogEntry
		var citedRaw, bubblesRaw, toolCallsRaw string
		if err := rows.Scan(
			&e.ID, &e.Kind, &e.TenantID, &e.ConversationID,
			&e.ContactPhone, &e.ContactName, &e.InboundText,
			&e.Answered, &e.AnsweredFromKb, &e.Handoff,
			&citedRaw, &bubblesRaw, &toolCallsRaw,
			&e.ProcessingMs, &e.Error, &e.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("ProcessingLogRepo.ListPaged scan: %w", err)
		}
		_ = json.Unmarshal([]byte(citedRaw), &e.CitedEntryIDs)
		_ = json.Unmarshal([]byte(bubblesRaw), &e.Bubbles)
		if toolCallsRaw != "null" {
			e.ToolCalls = json.RawMessage(toolCallsRaw)
		}
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
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
		SELECT id::text, COALESCE(kind, 'message'), tenant_id,
		       COALESCE(conversation_id::text, ''),
		       contact_phone, contact_name, inbound_text,
		       answered, answered_from_kb, handoff,
		       cited_entry_ids::text, bubbles::text,
		       COALESCE(tool_calls::text, 'null'),
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
		var citedRaw, bubblesRaw, toolCallsRaw string
		if err := rows.Scan(
			&e.ID, &e.Kind, &e.TenantID, &e.ConversationID,
			&e.ContactPhone, &e.ContactName, &e.InboundText,
			&e.Answered, &e.AnsweredFromKb, &e.Handoff,
			&citedRaw, &bubblesRaw, &toolCallsRaw,
			&e.ProcessingMs, &e.Error, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("ProcessingLogRepo.List scan: %w", err)
		}
		_ = json.Unmarshal([]byte(citedRaw), &e.CitedEntryIDs)
		_ = json.Unmarshal([]byte(bubblesRaw), &e.Bubbles)
		if toolCallsRaw != "null" {
			e.ToolCalls = json.RawMessage(toolCallsRaw)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
