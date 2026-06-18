-- name: InsertChargeItem :exec
INSERT INTO pay_charge_items (charge_id, product_id, name, price_cents, quantity)
VALUES ($1, $2, $3, $4, $5);
