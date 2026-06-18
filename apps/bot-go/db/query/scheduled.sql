-- name: ScheduledContactScheduleReactivation :exec
INSERT INTO scheduled_contacts (tenant_id, contact_id, conversation_id, fire_at, status, payload)
VALUES ($1, $2, $3, $4::date, 'pending', '{"kind":"reactivation"}'::jsonb)
ON CONFLICT DO NOTHING;

-- name: ScheduledContactPendingFollowUps :many
SELECT id, tenant_id, contact_id, conversation_id,
       payload->>'kind'  AS kind,
       status::text      AS status,
       fire_at,
       attempts,
       payload,
       created_at
FROM scheduled_contacts
WHERE payload->>'kind' = 'follow_up'
  AND status = 'pending'
  AND fire_at <= now()
ORDER BY fire_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: ScheduledContactMarkSent :exec
UPDATE scheduled_contacts
SET status     = 'fired',
    sent_at    = now(),
    updated_at = now()
WHERE id = $1;

-- name: ScheduledContactMarkFailed :exec
UPDATE scheduled_contacts
SET status            = 'failed',
    last_error        = $1,
    attempts          = attempts + 1,
    last_attempted_at = now(),
    updated_at        = now()
WHERE id = $2;

-- name: ScheduledContactScheduleFollowUp :exec
INSERT INTO scheduled_contacts (tenant_id, contact_id, conversation_id, fire_at, status, payload)
VALUES ($1, $2, $3, $4, 'pending', '{"kind":"follow_up"}'::jsonb)
ON CONFLICT DO NOTHING;

-- name: ScheduledContactCancelFollowUps :exec
UPDATE scheduled_contacts
SET status     = 'cancelled',
    updated_at = now()
WHERE tenant_id      = $1
  AND conversation_id = $2
  AND status          = 'pending'
  AND payload->>'kind' = 'follow_up';
