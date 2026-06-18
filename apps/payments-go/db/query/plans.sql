-- name: CreatePlan :one
INSERT INTO pay_plans (name, amount_cents, due_day)
VALUES ($1, $2, $3)
RETURNING id, name, amount_cents, due_day, active, created_at;

-- name: ListPlans :many
SELECT id, name, amount_cents, due_day, active
FROM pay_plans
ORDER BY name;
