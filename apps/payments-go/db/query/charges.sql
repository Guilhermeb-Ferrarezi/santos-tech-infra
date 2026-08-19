-- name: InsertCharge :one
INSERT INTO pay_charges
  (kind, subscription_id, student_id, customer_id, amount_cents, due_date, reference_month,
   provider, provider_charge_id, correlation_id, public_token, payer_tax_id, method,
   br_code, qr_code, pdf_url, barcode)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
RETURNING id, status, created_at;

-- name: InsertRecurrenceCharge :one
-- Cobrança de um ciclo de PIX Automático (kind='recorrente'), vinculada à recorrência.
-- O txid da cobr da Efí vai em provider_charge_id; reference_month garante a anti-duplicidade.
-- public_token só é preenchido na 1ª cobr da Jornada 3 (checkout recorrente, tela
-- /pay/{token}); nos ciclos seguintes (débito automático) vai NULL.
INSERT INTO pay_charges
  (kind, recurrence_id, customer_id, amount_cents, due_date, reference_month,
   provider, provider_charge_id, correlation_id, payer_tax_id, br_code, qr_code, public_token)
VALUES ('recorrente', $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id, status, created_at;

-- name: GetCharge :one
SELECT id, kind, subscription_id, student_id, amount_cents, due_date::text, reference_month,
       status, provider, COALESCE(provider_charge_id, ''), correlation_id,
       method, COALESCE(br_code, ''), COALESCE(qr_code, ''),
       COALESCE(pdf_url, ''), COALESCE(barcode, ''), refunded_cents, paid_at, created_at
FROM pay_charges
WHERE id = $1;

-- ListCharges filtra opcionalmente por status e student_id.
-- SQL dinâmico via condicionais inline ($1='' ignora filtro de status, $2=0 ignora filtro de aluno).
-- Não migrado para sqlc — ver store.go ListCharges.

-- name: GetChargeByPublicToken :one
SELECT id, kind, student_id, customer_id, amount_cents, due_date::text, status,
       method, COALESCE(br_code, ''), COALESCE(qr_code, ''),
       COALESCE(pdf_url, ''), COALESCE(barcode, ''),
       COALESCE(provider_charge_id, ''), correlation_id, paid_at, created_at
FROM pay_charges
WHERE public_token = $1;

-- name: MarkChargePaid :exec
-- Aceita 'expired' além de 'pending': o pagamento vem CONFIRMADO pelo gateway, e uma
-- cobrança que o job de expiração já fechou (ou que foi paga no limite da janela) não
-- pode ficar sem baixa — o dinheiro está na Efí de qualquer jeito.
UPDATE pay_charges
SET status = 'paid', paid_at = now()
WHERE correlation_id = $1 AND status IN ('pending', 'expired');

-- name: MarkChargePaidByProviderID :execrows
-- Boleto (API Cobranças): a notificação casa pelo charge_id do Efí (provider_charge_id),
-- não pelo correlation_id. Devolve nº de linhas afetadas (>0 = realmente marcou paga agora).
-- Aceita 'expired' pelo mesmo motivo de MarkChargePaid: boleto liquida em D+1 e o
-- gateway já confirmou o pagamento.
UPDATE pay_charges
SET status = 'paid', paid_at = now()
WHERE provider_charge_id = $1 AND status IN ('pending', 'expired');

-- name: PublicTokenByProviderID :one
SELECT public_token
FROM pay_charges
WHERE provider_charge_id = $1;

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

-- name: ListChargesByRecurrence :many
-- Ciclos (cobranças) de uma recorrência de PIX Automático, mais recentes primeiro.
SELECT id, kind, amount_cents, due_date::text, reference_month, status,
       COALESCE(br_code, ''), correlation_id, paid_at, created_at
FROM pay_charges
WHERE recurrence_id = $1
ORDER BY created_at DESC;

-- name: ListChargesByTaxID :many
-- Cobranças de TODAS as contas com um mesmo CPF (detalhe consolidado por pessoa).
SELECT c.id, c.kind, c.amount_cents, c.due_date::text, c.status,
       COALESCE(c.br_code, ''), c.correlation_id, c.paid_at, c.created_at
FROM pay_charges c
JOIN pay_customers cu ON cu.id = c.customer_id
WHERE cu.tax_id = $1
ORDER BY c.created_at DESC;

-- name: ListChargeItemsByTaxID :many
-- Itens das cobranças de todas as contas com um mesmo CPF.
SELECT ci.charge_id, ci.product_id, ci.name, ci.price_cents, ci.quantity
FROM pay_charge_items ci
JOIN pay_charges c ON c.id = ci.charge_id
JOIN pay_customers cu ON cu.id = c.customer_id
WHERE cu.tax_id = $1
ORDER BY ci.charge_id;

-- name: ExpireOverdueCharges :execrows
-- Marca como expiradas as cobranças PIX pendentes cujo QR já passou da validade
-- (vencimento + 23h, igual à expiração enviada à Efí em createAndPersistCharge).
--
-- O filtro method = 'pix' é obrigatório: a janela de 23h é a do QR do PIX e não vale
-- para os outros meios. Sem ele, cartão (que nasce com due_date = hoje) expirava em
-- ~23h e boleto liquidado em D+1 já chegava expirado — e como MarkChargePaidByProviderID
-- não casava com 'expired', a confirmação virava no-op: dinheiro na Efí, cobrança
-- expirada no banco.
UPDATE pay_charges
SET status = 'expired'
WHERE status = 'pending'
  AND method = 'pix'
  AND (due_date + interval '23 hours') < now();

-- name: CancelChargeByToken :one
-- Cancela uma cobrança pendente pelo token público (botão na tela de pagamento).
-- Só afeta pendentes; já paga/cancelada/inexistente → sem linha (ErrNoRows).
UPDATE pay_charges
SET status = 'canceled'
WHERE public_token = $1 AND status = 'pending'
RETURNING correlation_id, COALESCE(provider_charge_id, '') AS provider_charge_id, method;

-- GetStats usa SQL com fragmento dinâmico (WHERE clause montada em runtime).
-- Não migrado para sqlc — ver store.go GetStats.
