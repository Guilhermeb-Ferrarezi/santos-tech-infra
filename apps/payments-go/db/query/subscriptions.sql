-- name: CreateSubscription :one
INSERT INTO pay_subscriptions (student_id, plan_id, amount_cents, due_day)
VALUES ($1, $2, $3, $4)
RETURNING id, student_id, plan_id, amount_cents, due_day, status, created_at;

-- name: ListSubscriptions :many
SELECT id, student_id, plan_id, amount_cents, due_day, status
FROM pay_subscriptions
ORDER BY id;

-- name: SetSubscriptionStatus :exec
UPDATE pay_subscriptions SET status = $2 WHERE id = $1;

-- name: SubscriptionsDueToday :many
SELECT sub.id, sub.student_id, sub.plan_id, sub.amount_cents, sub.due_day, sub.status,
       st.id         AS student_id_2,
       st.name       AS student_name,
       st.tax_id     AS student_tax_id,
       st.email      AS student_email,
       st.phone      AS student_phone,
       st.created_at AS student_created_at
FROM pay_subscriptions sub
JOIN pay_students st ON st.id = sub.student_id
WHERE sub.status = 'active'
  AND sub.due_day = $1
  AND NOT EXISTS (
    SELECT 1 FROM pay_charges c
    WHERE c.subscription_id = sub.id
      AND c.reference_month = $2
  );
