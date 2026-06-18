-- name: MarkWebhookSeen :execrows
INSERT INTO pay_webhook_events (id, type, payload)
VALUES ($1, $2, $3)
ON CONFLICT (id) DO NOTHING;
