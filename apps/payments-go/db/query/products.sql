-- name: CreateProduct :one
INSERT INTO pay_products (slug, name, description, price_cents, recurring, periodicity, due_day, charge_on_subscribe, image_url, file_url)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, slug, name, description, price_cents, active, recurring, periodicity, due_day, charge_on_subscribe, COALESCE(image_url, '') AS image_url, COALESCE(file_url, '') AS file_url, created_at;

-- name: ListProducts :many
SELECT id, slug, name, description, price_cents, active, recurring, periodicity, due_day, charge_on_subscribe, COALESCE(image_url, '') AS image_url, COALESCE(file_url, '') AS file_url
FROM pay_products
ORDER BY name;

-- name: GetProductBySlug :one
SELECT id, slug, name, description, price_cents, active, recurring, periodicity, due_day, charge_on_subscribe, COALESCE(image_url, '') AS image_url, COALESCE(file_url, '') AS file_url
FROM pay_products
WHERE slug = $1 AND active = true;

-- name: GetProductByID :one
SELECT id, slug, name, description, price_cents, active, recurring, periodicity, due_day, charge_on_subscribe, COALESCE(image_url, '') AS image_url, COALESCE(file_url, '') AS file_url
FROM pay_products
WHERE id = $1 AND active = true;

-- name: UpdateProduct :execrows
UPDATE pay_products
SET name = $2, description = $3, price_cents = $4, active = $5, recurring = $6, periodicity = $7, due_day = $8, charge_on_subscribe = $9, image_url = $10, file_url = $11
WHERE id = $1;

-- name: DeleteProduct :execrows
DELETE FROM pay_products WHERE id = $1;

-- name: UserOwnsProduct :one
-- Direito de acesso ao entregável (file_url): existe alguma cobrança PAGA do usuário
-- que inclua este produto, seja como item de compra avulsa, seja como ciclo de uma
-- assinatura dele. Nunca sirva o arquivo sem passar por aqui.
SELECT EXISTS (
  SELECT 1
  FROM pay_charge_items ci
  JOIN pay_charges   c  ON c.id = ci.charge_id
  JOIN pay_customers cu ON cu.id = c.customer_id
  WHERE cu.user_id = $1 AND ci.product_id = $2 AND c.status = 'paid'
  UNION ALL
  SELECT 1
  FROM pay_recurrences r
  JOIN pay_customers cu ON cu.id = r.customer_id
  JOIN pay_charges   c  ON c.recurrence_id = r.id
  WHERE cu.user_id = $1 AND r.product_id = $2 AND c.status = 'paid'
)::bool AS owns;
