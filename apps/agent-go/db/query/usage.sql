-- name: InsertUsageEvent :exec
INSERT INTO claude_usage_events (
  source, task, model, conversation_id,
  total_cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
  duration_ms, is_error, origin, billing
) VALUES (
  $1, $2, $3, $4,
  $5, $6, $7, $8, $9,
  $10, $11, $12, $13
);

-- name: UsageSummary :one
SELECT
  COALESCE(SUM(total_cost_usd), 0)::float8 AS total_cost_usd,
  COUNT(*)::bigint AS calls,
  COALESCE(SUM(input_tokens), 0)::bigint AS input_tokens,
  COALESCE(SUM(output_tokens), 0)::bigint AS output_tokens,
  COALESCE(SUM(cache_read_tokens), 0)::bigint AS cache_read_tokens,
  COALESCE(SUM(cache_write_tokens), 0)::bigint AS cache_write_tokens,
  COALESCE(SUM(total_cost_usd) FILTER (WHERE billing = 'api_key'), 0)::float8 AS api_cost_usd
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

-- name: UsageSince :one
-- Data do primeiro registro — é o "desde quando" do acumulado no painel.
SELECT MIN(created_at)::timestamptz AS first_at FROM claude_usage_events;

-- name: UsageByOrigin :many
-- Gasto por FUNÇÃO (quem pediu): sessão interativa → 'sessao'; senão a origem
-- declarada pelo chamador ("bot", "posaula", ...); senão a task. O "raw" que sobra
-- é o histórico de antes da coluna origin existir (não dá pra reatribuir).
-- api_* = só o que rodou com chave de API (custo real); o resto é simulação.
SELECT
  (CASE WHEN source = 'session' THEN 'sessao'
        WHEN origin <> '' THEN origin
        ELSE task END)::text AS origin_key,
  COALESCE(SUM(total_cost_usd), 0)::float8 AS cost_usd,
  COUNT(*)::bigint AS calls,
  COALESCE(SUM(input_tokens), 0)::bigint AS input_tokens,
  COALESCE(SUM(output_tokens), 0)::bigint AS output_tokens,
  COALESCE(SUM(cache_read_tokens), 0)::bigint AS cache_read_tokens,
  COALESCE(SUM(cache_write_tokens), 0)::bigint AS cache_write_tokens,
  COALESCE(SUM(total_cost_usd) FILTER (WHERE created_at >= sqlc.arg(week_since)), 0)::float8 AS week_cost_usd,
  (COUNT(*) FILTER (WHERE created_at >= sqlc.arg(week_since)))::bigint AS week_calls,
  COALESCE(SUM(total_cost_usd) FILTER (WHERE billing = 'api_key'), 0)::float8 AS api_cost_usd,
  (COUNT(*) FILTER (WHERE billing = 'api_key'))::bigint AS api_calls
FROM claude_usage_events
WHERE created_at >= sqlc.arg(since)
GROUP BY origin_key
ORDER BY cost_usd DESC;
