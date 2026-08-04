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
  COUNT(*)::bigint AS calls
FROM claude_usage_events
WHERE created_at >= $1;

-- name: UsageDaily :many
SELECT
  date_trunc('day', created_at)::date AS day,
  COALESCE(SUM(total_cost_usd), 0)::float8 AS cost_usd,
  COUNT(*)::bigint AS calls
FROM claude_usage_events
WHERE created_at >= $1
GROUP BY day
ORDER BY day;

-- name: UsageBySource :many
SELECT
  source,
  COALESCE(SUM(total_cost_usd), 0)::float8 AS cost_usd,
  COUNT(*)::bigint AS calls
FROM claude_usage_events
WHERE created_at >= $1
GROUP BY source
ORDER BY cost_usd DESC;
