-- name: PendingQuestionInsert :exec
INSERT INTO pending_question
  (tenant_id, conversation_id, client_phone, client_name, question, channel, status)
SELECT $1, $2, $3, $4, $5, $6, 'open'
WHERE NOT EXISTS (
    SELECT 1 FROM pending_question
    WHERE tenant_id = $1
      AND conversation_id = $2
      AND status IN ('open', 'drafted')
);

-- name: PendingQuestionListOpen :many
SELECT id, conversation_id, client_phone, client_name,
       question, status,
       COALESCE(draft, '')                    AS draft,
       COALESCE(channel, 'whatsapp')          AS channel,
       created_at
FROM pending_question
WHERE tenant_id = $1 AND status IN ('open', 'drafted')
ORDER BY created_at;

-- name: PendingQuestionGet :one
SELECT conversation_id, client_phone, client_name,
       question, status,
       COALESCE(draft, '')           AS draft,
       COALESCE(channel, 'whatsapp') AS channel,
       created_at
FROM pending_question
WHERE tenant_id = $1 AND id = $2 AND status IN ('open', 'drafted');

-- name: PendingQuestionStoreDraft :exec
UPDATE pending_question
SET draft      = $3,
    status     = 'drafted',
    updated_at = now()
WHERE tenant_id = $1 AND id = $2 AND status IN ('open', 'drafted');

-- name: PendingQuestionMarkResolved :exec
UPDATE pending_question SET status = 'resolved', updated_at = now()
WHERE tenant_id = $1 AND id = $2;

-- name: PendingBookingInsert :exec
INSERT INTO pending_booking
  (tenant_id, conversation_id, client_phone, client_name, student_name, kind, course,
   proposed_day, proposed_date, proposed_time, proposed_period, age, notes, channel, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'open');

-- name: PendingBookingListOpen :many
SELECT id, conversation_id, client_phone, client_name,
       COALESCE(student_name, '')    AS student_name,
       kind, course,
       proposed_day,
       COALESCE(proposed_date, '')   AS proposed_date,
       proposed_time, proposed_period,
       COALESCE(age, 0)              AS age,
       notes, status,
       COALESCE(channel, 'whatsapp') AS channel,
       created_at
FROM pending_booking
WHERE tenant_id = $1 AND status = 'open'
ORDER BY created_at;

-- name: PendingBookingGet :one
SELECT conversation_id, client_phone, client_name,
       COALESCE(student_name, '')    AS student_name,
       kind, course,
       proposed_day,
       COALESCE(proposed_date, '')   AS proposed_date,
       proposed_time, proposed_period,
       COALESCE(age, 0)              AS age,
       notes, status,
       COALESCE(channel, 'whatsapp') AS channel,
       created_at
FROM pending_booking
WHERE tenant_id = $1 AND id = $2 AND status = 'open';

-- name: PendingBookingMarkStatus :exec
UPDATE pending_booking SET status = $3, updated_at = now()
WHERE tenant_id = $1 AND id = $2;

-- name: PendingBookingUpdateProposed :exec
UPDATE pending_booking
SET proposed_date = $3,
    proposed_time = $4,
    proposed_day  = '',
    updated_at    = now()
WHERE tenant_id = $1 AND id = $2;
