-- name: InsertCharge :one
INSERT INTO pay_charges
  (kind, subscription_id, student_id, customer_id, amount_cents, due_date, reference_month,
   provider, provider_charge_id, correlation_id, public_token, payer_tax_id, br_code, qr_code)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING id, status, created_at;

-- name: GetCharge :one
SELECT id, kind, subscription_id, student_id, amount_cents, due_date::text, reference_month,
       status, provider, COALESCE(provider_charge_id, ''), correlation_id,
       COALESCE(br_code, ''), COALESCE(qr_code, ''), paid_at, created_at
FROM pay_charges
WHERE id = $1;

-- ListCharges filtra opcionalmente por status e student_id.
-- SQL dinâmico via condicionais inline ($1='' ignora filtro de status, $2=0 ignora filtro de aluno).
-- Não migrado para sqlc — ver store.go ListCharges.

-- name: GetChargeByPublicToken :one
SELECT id, kind, student_id, customer_id, amount_cents, due_date::text, status,
       COALESCE(br_code, ''), COALESCE(qr_code, ''), correlation_id, paid_at, created_at
FROM pay_charges
WHERE public_token = $1;

-- name: MarkChargePaid :exec
UPDATE pay_charges
SET status = 'paid', paid_at = now()
WHERE correlation_id = $1 AND status = 'pending';

-- name: MarkChargeExpired :exec
UPDATE pay_charges
SET status = 'expired'
WHERE correlation_id = $1 AND status = 'pending';

-- name: PublicTokenByCorrelation :one
SELECT public_token
FROM pay_charges
WHERE correlation_id = $1;

-- name: PayerEmailByCharge :one
SELECT COALESCE(NULLIF(st.name, ''),  cu.name, '') AS name,
       COALESCE(NULLIF(st.email, ''), cu.email, '') AS email
FROM pay_charges c
LEFT JOIN pay_students  st ON st.id = c.student_id
LEFT JOIN pay_customers cu ON cu.id = c.customer_id
WHERE c.id = $1;

-- name: ListChargesByCustomer :many
SELECT id, kind, amount_cents, due_date::text, status,
       COALESCE(br_code, ''), correlation_id, paid_at, created_at
FROM pay_charges
WHERE customer_id = $1
ORDER BY created_at DESC;

-- name: ListChargeItemsByCustomer :many
-- Itens de todas as cobranças de um cliente (para montar o detalhe das compras).
SELECT ci.charge_id, ci.product_id, ci.name, ci.price_cents, ci.quantity
FROM pay_charge_items ci
JOIN pay_charges c ON c.id = ci.charge_id
WHERE c.customer_id = $1
ORDER BY ci.charge_id;

-- GetStats usa SQL com fragmento dinâmico (WHERE clause montada em runtime).
-- Não migrado para sqlc — ver store.go GetStats.
