-- Queries do roteador de chaves de API (failover automático em 401/sem-créditos)

-- name: ListAPIRouterProviders :many
SELECT id, name, base_url, auth_header, auth_scheme, unauthorized_codes, no_credit_codes, test_path, test_method, chat_adapter, created_at, updated_at
FROM api_router_providers ORDER BY name ASC;

-- name: GetAPIRouterProvider :one
SELECT id, name, base_url, auth_header, auth_scheme, unauthorized_codes, no_credit_codes, test_path, test_method, chat_adapter, created_at, updated_at
FROM api_router_providers WHERE id = $1;

-- name: InsertAPIRouterProvider :one
INSERT INTO api_router_providers (name, base_url, auth_header, auth_scheme, unauthorized_codes, no_credit_codes, test_path, test_method, chat_adapter)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, name, base_url, auth_header, auth_scheme, unauthorized_codes, no_credit_codes, test_path, test_method, chat_adapter, created_at, updated_at;

-- name: UpdateAPIRouterProvider :one
UPDATE api_router_providers SET
  name = $2, base_url = $3, auth_header = $4, auth_scheme = $5,
  unauthorized_codes = $6, no_credit_codes = $7, test_path = $8, test_method = $9,
  chat_adapter = $10, updated_at = now()
WHERE id = $1
RETURNING id, name, base_url, auth_header, auth_scheme, unauthorized_codes, no_credit_codes, test_path, test_method, chat_adapter, created_at, updated_at;

-- name: DeleteAPIRouterProvider :execrows
DELETE FROM api_router_providers WHERE id = $1;

-- name: ListAPIRouterKeys :many
SELECT id, provider_id, label, secret_enc, secret_tail, status, priority, failure_count, last_used_at, last_error_at, last_error_code, created_at, updated_at
FROM api_router_keys WHERE provider_id = $1 ORDER BY priority ASC, id ASC;

-- name: ListActiveAPIRouterKeys :many
SELECT id, provider_id, label, secret_enc, secret_tail, status, priority, failure_count, last_used_at, last_error_at, last_error_code, created_at, updated_at
FROM api_router_keys WHERE provider_id = $1 AND status = 'active' ORDER BY priority ASC, id ASC;

-- name: GetAPIRouterKey :one
SELECT id, provider_id, label, secret_enc, secret_tail, status, priority, failure_count, last_used_at, last_error_at, last_error_code, created_at, updated_at
FROM api_router_keys WHERE id = $1 AND provider_id = $2;

-- name: InsertAPIRouterKey :one
INSERT INTO api_router_keys (provider_id, label, secret_enc, secret_tail, priority)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, provider_id, label, secret_enc, secret_tail, status, priority, failure_count, last_used_at, last_error_at, last_error_code, created_at, updated_at;

-- name: UpdateAPIRouterKeyMeta :one
UPDATE api_router_keys SET label = $3, priority = $4, updated_at = now()
WHERE id = $1 AND provider_id = $2
RETURNING id, provider_id, label, secret_enc, secret_tail, status, priority, failure_count, last_used_at, last_error_at, last_error_code, created_at, updated_at;

-- name: SetAPIRouterKeyStatus :one
UPDATE api_router_keys SET
  status = $3,
  failure_count = CASE WHEN $3 = 'active' THEN 0 ELSE failure_count END,
  updated_at = now()
WHERE id = $1 AND provider_id = $2
RETURNING id, provider_id, label, secret_enc, secret_tail, status, priority, failure_count, last_used_at, last_error_at, last_error_code, created_at, updated_at;

-- name: RecordAPIRouterKeySuccess :exec
UPDATE api_router_keys SET status = 'active', failure_count = 0, last_used_at = now(), updated_at = now()
WHERE id = $1;

-- name: RecordAPIRouterKeyFailure :exec
UPDATE api_router_keys SET
  status = $2, failure_count = failure_count + 1,
  last_error_at = now(), last_error_code = $3, last_used_at = now(), updated_at = now()
WHERE id = $1;

-- name: DeleteAPIRouterKey :execrows
DELETE FROM api_router_keys WHERE id = $1 AND provider_id = $2;

-- name: CountAPIRouterKeysByProvider :many
SELECT provider_id, status, count(*) AS total FROM api_router_keys GROUP BY provider_id, status;
