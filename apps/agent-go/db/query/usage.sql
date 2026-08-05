-- name: InsertUsageEvent :exec
INSERT INTO claude_usage_events (
  source, task, model, conversation_id,
  total_cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
  duration_ms, is_error
) VALUES (
  $1, $2, $3, $4,
  $5, $6, $7, $8, $9,
  $10, $11
);

-- name: UsageSummary :one
SELECT
  COALESCE(SUM(total_cost_usd), 0)::float8 AS total_cost_usd,
  COUNT(*)::bigint AS calls,
  COALESCE(SUM(input_tokens), 0)::bigint AS input_tokens,
  COALESCE(SUM(output_tokens), 0)::bigint AS output_tokens,
  COALESCE(SUM(cache_read_tokens), 0)::bigint AS cache_read_tokens,
  COALESCE(SUM(cache_write_tokens), 0)::bigint AS cache_write_tokens
FROM claude_usage_events
WHERE created_at >= $1;

-- name: UsageDaily :many
SELECT
  date_trunc('day', created_at)::date AS day,
  COALESCE(SUM(total_cost_usd), 0)::float8 AS cost_usd,
  COUNT(*)::bigint AS calls,
  COALESCE(SUM(input_tokens + output_tokens), 0)::bigint AS tokens
FROM claude_usage_events
WHERE created_at >= $1
GROUP BY day
ORDER BY day;

-- name: UsageBySource :many
SELECT
  source,
  COALESCE(SUM(total_cost_usd), 0)::float8 AS cost_usd,
  COUNT(*)::bigint AS calls,
  COALESCE(SUM(input_tokens + output_tokens), 0)::bigint AS tokens
FROM claude_usage_events
WHERE created_at >= $1
GROUP BY source
ORDER BY cost_usd DESC;

-- name: UsageByTask :many
-- "task" só é preenchido pelas chamadas de /claude/generate (email, raw = bot do
-- WhatsApp, diagram, ...); sessões interativas (source='session') ficam de fora
-- (task='') — é o que permite separar quanto o bot pesa vs. o resto no painel.
SELECT
  task,
  COALESCE(SUM(total_cost_usd), 0)::float8 AS cost_usd,
  COUNT(*)::bigint AS calls,
  COALESCE(SUM(input_tokens), 0)::bigint AS input_tokens,
  COALESCE(SUM(output_tokens), 0)::bigint AS output_tokens,
  COALESCE(SUM(cache_read_tokens), 0)::bigint AS cache_read_tokens,
  COALESCE(SUM(cache_write_tokens), 0)::bigint AS cache_write_tokens
FROM claude_usage_events
WHERE created_at >= $1 AND task <> ''
GROUP BY task
ORDER BY cost_usd DESC;
