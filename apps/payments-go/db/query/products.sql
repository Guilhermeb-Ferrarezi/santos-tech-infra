-- name: CreateProduct :one
INSERT INTO pay_products (slug, name, description, price_cents, recurring, periodicity, due_day, charge_on_subscribe)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, slug, name, description, price_cents, active, recurring, periodicity, due_day, charge_on_subscribe, created_at;

-- name: ListProducts :many
SELECT id, slug, name, description, price_cents, active, recurring, periodicity, due_day, charge_on_subscribe
FROM pay_products
ORDER BY name;

-- name: GetProductBySlug :one
SELECT id, slug, name, description, price_cents, active, recurring, periodicity, due_day, charge_on_subscribe
FROM pay_products
WHERE slug = $1 AND active = true;

-- name: GetProductByID :one
SELECT id, slug, name, description, price_cents, active, recurring, periodicity, due_day, charge_on_subscribe
FROM pay_products
WHERE id = $1 AND active = true;

-- name: UpdateProduct :execrows
UPDATE pay_products
SET name = $2, description = $3, price_cents = $4, active = $5, recurring = $6, periodicity = $7, due_day = $8, charge_on_subscribe = $9
WHERE id = $1;

-- name: DeleteProduct :execrows
DELETE FROM pay_products WHERE id = $1;
