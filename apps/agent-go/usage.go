package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	agentdb "github.com/santos-tech/agent/db"
)

// usageFields resume custo/consumo extraído de um evento "result" do claude CLI
// (--output-format json ou stream-json) — mesmo formato nos dois modos.
type usageFields struct {
	TotalCostUSD     float64
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	DurationMS       int64
	IsError          bool
}

// usageFromMap extrai os campos de custo de um evento "result" já decodificado
// (map[string]any, vindo tanto do envelope --output-format json quanto da última
// linha do stream-json). Campos ausentes ficam zerados — nunca falha.
func usageFromMap(ev map[string]any) usageFields {
	var f usageFields
	if v, ok := ev["total_cost_usd"].(float64); ok {
		f.TotalCostUSD = v
	}
	if v, ok := ev["duration_ms"].(float64); ok {
		f.DurationMS = int64(v)
	}
	if v, ok := ev["is_error"].(bool); ok {
		f.IsError = v
	}
	if u, ok := ev["usage"].(map[string]any); ok {
		f.InputTokens = int64FromAny(u["input_tokens"])
		f.OutputTokens = int64FromAny(u["output_tokens"])
		f.CacheReadTokens = int64FromAny(u["cache_read_input_tokens"])
		f.CacheWriteTokens = int64FromAny(u["cache_creation_input_tokens"])
	}
	return f
}

func int64FromAny(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

// usageMeta identifica uma invocação do CLI no painel de gastos.
type usageMeta struct {
	source  string // 'generate' | 'generate_stream' | 'session'
	task    string // task do /claude/generate ("raw", "email", ...)
	origin  string // quem pediu ("bot", "posaula", ...) — "" quando não declarado
	billing string // credencial que rodou: billingSubscription | billingAPIKey
	model   string
	convID  string // "" = sem conversa (generate/generate_stream são stateless)
}

// recordUsage persiste uma invocação do CLI claude para o painel de gastos. Nunca
// propaga erro pro chamador (best-effort: falha em registrar custo não pode derrubar
// a geração) — só loga um warning.
func (s *Server) recordUsage(ctx context.Context, m usageMeta, f usageFields) {
	if m.billing == "" {
		m.billing = billingSubscription
	}
	if s.onUsage != nil {
		s.onUsage(m, f)
	}
	if s.q == nil {
		return
	}
	var conv pgtype.UUID
	if m.convID != "" {
		conv = uuidFromStr(m.convID)
	}
	if err := s.q.InsertUsageEvent(ctx, agentdb.InsertUsageEventParams{
		Source:           m.source,
		Task:             m.task,
		Model:            m.model,
		ConversationID:   conv,
		TotalCostUsd:     f.TotalCostUSD,
		InputTokens:      f.InputTokens,
		OutputTokens:     f.OutputTokens,
		CacheReadTokens:  f.CacheReadTokens,
		CacheWriteTokens: f.CacheWriteTokens,
		DurationMs:       f.DurationMS,
		IsError:          f.IsError,
		Origin:           m.origin,
		Billing:          m.billing,
	}); err != nil {
		slog.Warn("recordUsage: insert falhou", "source", m.source, "err", err)
	}
}
