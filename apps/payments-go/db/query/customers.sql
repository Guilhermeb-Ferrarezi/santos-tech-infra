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
