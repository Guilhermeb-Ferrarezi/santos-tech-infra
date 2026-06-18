-- name: UpsertCustomer :one
INSERT INTO pay_customers (user_id)
VALUES ($1)
ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id
RETURNING id, user_id, tax_id, phone, name, email;

-- name: GetCustomerByUserID :one
SELECT id, user_id, tax_id, phone, name, email
FROM pay_customers
WHERE user_id = $1;

-- name: UpdateCustomerData :exec
UPDATE pay_customers
SET tax_id = $2, phone = $3, name = $4, email = $5
WHERE user_id = $1;
