-- name: UpsertCustomer :one
-- Cliente único por (user_id, tax_id): cria ou atualiza os dados do pagador.
INSERT INTO pay_customers (user_id, tax_id, phone, name, email)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, tax_id) DO UPDATE
  SET phone = EXCLUDED.phone, name = EXCLUDED.name, email = EXCLUDED.email
RETURNING id, user_id, tax_id, phone, name, email;

-- name: GetCustomerByUserID :one
-- Como a conta pode ter mais de um cliente (um por CPF), devolve o mais recente.
SELECT id, user_id, tax_id, phone, name, email
FROM pay_customers
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: GetCustomerByID :one
SELECT id, user_id, tax_id, phone, name, email, created_at
FROM pay_customers
WHERE id = $1;

-- name: ListCustomersWithStats :many
-- Lista clientes com agregados das cobranças (admin): nº de compras, nº/total pago,
-- e data da última compra. LEFT JOIN para incluir clientes ainda sem cobrança.
SELECT cu.id, cu.user_id, cu.tax_id, cu.phone, cu.name, cu.email, cu.created_at,
       (COUNT(c.id))::bigint AS charges_count,
       (COUNT(c.id) FILTER (WHERE c.status = 'paid'))::bigint AS paid_count,
       (COALESCE(SUM(c.amount_cents) FILTER (WHERE c.status = 'paid'), 0))::bigint AS paid_total_cents,
       MAX(c.created_at)::timestamptz AS last_charge_at
FROM pay_customers cu
LEFT JOIN pay_charges c ON c.customer_id = cu.id
GROUP BY cu.id
ORDER BY MAX(c.created_at) DESC NULLS LAST, cu.created_at DESC;
