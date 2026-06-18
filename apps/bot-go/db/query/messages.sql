-- name: InboundMessageRecord :execrows
INSERT INTO inbound_message (tenant_id, conversation_id, provider_message_id, content, received_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (tenant_id, provider_message_id) DO NOTHING;

-- name: OutboundMessageRecord :exec
INSERT INTO outbound_message
  (tenant_id, conversation_id, provider_message_id, idempotency_key, intent_category, content, status, sent_at, reasoning)
VALUES ($1, $2, $3, $4, 'FREE_FORM'::outbound_intent, $5, 'sent', now(), $6)
ON CONFLICT (tenant_id, idempotency_key) DO NOTHING;

-- name: WebhookSetNextRetryAt :exec
UPDATE webhook_events
SET next_retry_at = $1, processing_status = 'failed'
WHERE provider_event_id = $2;

-- name: ConversationGetRecentTurns :many
SELECT role, text FROM (
    SELECT 'user'                                  AS role,
           COALESCE(im.content->>'Text', '[mídia]') AS text,
           im.received_at                           AS ts
    FROM inbound_message im
    WHERE im.conversation_id = $1

    UNION ALL

    SELECT 'assistant'         AS role,
           om.content->>'Text' AS text,
           om.created_at       AS ts
    FROM outbound_message om
    WHERE om.conversation_id = $1
      AND om.content->>'Text' IS NOT NULL
) sub
ORDER BY ts DESC
LIMIT $2;

-- name: OutboundMessageInsertForSend :exec
INSERT INTO outbound_message
    (tenant_id, conversation_id, provider_message_id, idempotency_key, intent_category, content, status, sent_at)
VALUES ($1, $2, $3, $4, 'FREE_FORM'::outbound_intent, $5, 'sent', now())
ON CONFLICT (tenant_id, idempotency_key) DO NOTHING;
