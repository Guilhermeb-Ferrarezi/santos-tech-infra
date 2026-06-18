-- name: DomainEventEmit :exec
INSERT INTO domain_events (tenant_id, event_type, aggregate_id, payload, occurred_at)
VALUES ($1, $2, $3, $4, $5);

-- name: DomainEventDrain :many
SELECT id, tenant_id, aggregate_id, event_type, payload, occurred_at
FROM domain_events
WHERE processed_at IS NULL
ORDER BY occurred_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: DomainEventMarkProcessed :exec
UPDATE domain_events SET processed_at = now() WHERE id = $1;

-- name: DomainEventMarkFailed :exec
UPDATE domain_events
SET attempts   = attempts + 1,
    last_error = $1
WHERE id = $2;

-- name: OutboundMessageInsert :execrows
INSERT INTO outbound_message
  (tenant_id, conversation_id, idempotency_key, intent_category, content, template_payload)
VALUES ($1, $2, $3, $4::outbound_intent, $5, $6)
ON CONFLICT (tenant_id, idempotency_key) DO NOTHING;

-- name: OutboundMessageClaim :execrows
UPDATE outbound_message
SET status     = 'sent',
    sent_at    = now(),
    updated_at = now()
WHERE tenant_id = $1 AND idempotency_key = $2 AND status = 'pending';

-- name: WebhookEventRecord :one
INSERT INTO webhook_events (tenant_id, provider, provider_event_id, raw_payload, raw_body)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tenant_id, provider, provider_event_id) DO NOTHING
RETURNING id;

-- name: WebhookEventFindDuplicate :one
SELECT id FROM webhook_events
WHERE tenant_id = $1 AND provider = $2 AND provider_event_id = $3;

-- name: WebhookEventMarkDone :exec
UPDATE webhook_events
SET processing_status = 'done', processed_at = now()
WHERE id = $1;

-- name: WebhookEventMarkFailed :exec
UPDATE webhook_events
SET processing_status = 'failed',
    last_error        = left($1, 500),
    attempts          = attempts + 1,
    next_retry_at     = now() + (power(2, attempts) * interval '1 minute')
WHERE id = $2;

-- name: WebhookEventPendingRetries :many
SELECT id, tenant_id, provider, provider_event_id, raw_payload,
       processing_status::text AS processing_status,
       attempts, next_retry_at, last_error,
       processed_at, received_at
FROM webhook_events
WHERE processing_status = 'failed'
  AND (next_retry_at IS NULL OR next_retry_at <= now())
ORDER BY next_retry_at NULLS FIRST
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: WebhookEventGetByID :one
SELECT id, tenant_id, provider, provider_event_id, raw_payload,
       processing_status::text AS processing_status,
       attempts, next_retry_at, last_error,
       processed_at, received_at
FROM webhook_events
WHERE id = $1;
