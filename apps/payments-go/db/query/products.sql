-- name: CreateProduct :one
INSERT INTO pay_products (slug, name, description, price_cents)
VALUES ($1, $2, $3, $4)
RETURNING id, slug, name, description, price_cents, active, created_at;

-- name: ListProducts :many
SELECT id, slug, name, description, price_cents, active
FROM pay_products
ORDER BY name;

-- name: GetProductBySlug :one
SELECT id, slug, name, description, price_cents, active
FROM pay_products
WHERE slug = $1 AND active = true;

-- name: GetProductByID :one
SELECT id, slug, name, description, price_cents, active
FROM pay_products
WHERE id = $1 AND active = true;

-- name: UpdateProduct :execrows
UPDATE pay_products
SET name = $2, description = $3, price_cents = $4, active = $5
WHERE id = $1;

-- name: DeleteProduct :execrows
DELETE FROM pay_products WHERE id = $1;
