-- name: CreateRecurrence :one
INSERT INTO pay_recurrences
  (subscription_id, product_id, customer_id, payer_tax_id, payer_name, amount_cents,
   periodicity, due_day, start_date, end_date, journey)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING id, status, created_at;

-- name: GetRecurrence :one
SELECT id, subscription_id, product_id, customer_id, payer_tax_id, payer_name,
       amount_cents, periodicity, due_day, start_date::text, end_date::text, journey,
       COALESCE(efi_id_rec, ''), COALESCE(br_code, ''), COALESCE(qr_code, ''),
       status, created_at
FROM pay_recurrences
WHERE id = $1;

-- name: GetRecurrenceByEfiID :one
SELECT id, subscription_id, product_id, customer_id, payer_tax_id, payer_name,
       amount_cents, periodicity, due_day, start_date::text, end_date::text, journey,
       COALESCE(efi_id_rec, ''), COALESCE(br_code, ''), COALESCE(qr_code, ''),
       status, created_at
FROM pay_recurrences
WHERE efi_id_rec = $1;

-- name: ListRecurrences :many
SELECT id, subscription_id, product_id, customer_id, payer_tax_id, payer_name,
       amount_cents, periodicity, due_day, start_date::text, end_date::text, journey,
       COALESCE(efi_id_rec, ''), COALESCE(br_code, ''), COALESCE(qr_code, ''),
       status, created_at
FROM pay_recurrences
ORDER BY created_at DESC;

-- name: SetRecurrenceStatus :exec
UPDATE pay_recurrences SET status = $2 WHERE id = $1;

-- name: UpdateRecurrenceAuth :exec
-- Grava o resultado da criação na Efí (idRec + QR/copia-e-cola) e ativa o status.
UPDATE pay_recurrences
SET efi_id_rec = $2, br_code = $3, qr_code = $4, status = $5
WHERE id = $1;

-- name: RecurrencesDueToday :many
-- Recorrências 'active' cujo ciclo vence hoje (due_day == dia atual) e que ainda não
-- têm a cobrança do ciclo (anti-duplicidade por reference_month, espelha SubscriptionsDueToday).
SELECT id, subscription_id, product_id, customer_id, payer_tax_id, payer_name,
       amount_cents, periodicity, due_day, start_date::text, end_date::text, journey,
       COALESCE(efi_id_rec, ''), COALESCE(br_code, ''), COALESCE(qr_code, ''),
       status, created_at
FROM pay_recurrences r
WHERE r.status = 'active'
  AND r.due_day = $1
  AND (r.end_date IS NULL OR r.end_date >= current_date)
  AND NOT EXISTS (
    SELECT 1 FROM pay_charges c
    WHERE c.recurrence_id = r.id
      AND c.reference_month = $2
  );
