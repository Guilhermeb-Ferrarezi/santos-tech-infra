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

// recordUsage persiste uma invocação do CLI claude para o painel de gastos. Nunca
// propaga erro pro chamador (best-effort: falha em registrar custo não pode derrubar
// a geração) — só loga um warning. convID vazio = sem conversa associada (source
// "generate"/"generate_stream", que são stateless).
func (s *Server) recordUsage(ctx context.Context, source, task, model, convID string, f usageFields) {
	if s.q == nil {
		return
	}
	var conv pgtype.UUID
	if convID != "" {
		conv = uuidFromStr(convID)
	}
	if err := s.q.InsertUsageEvent(ctx, agentdb.InsertUsageEventParams{
		Source:           source,
		Task:             task,
		Model:            model,
		ConversationID:   conv,
		TotalCostUsd:     f.TotalCostUSD,
		InputTokens:      f.InputTokens,
		OutputTokens:     f.OutputTokens,
		CacheReadTokens:  f.CacheReadTokens,
		CacheWriteTokens: f.CacheWriteTokens,
		DurationMs:       f.DurationMS,
		IsError:          f.IsError,
	}); err != nil {
		slog.Warn("recordUsage: insert falhou", "source", source, "err", err)
	}
}
