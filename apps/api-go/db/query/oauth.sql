-- Queries de OAuth Clients (aplicações "Entrar com Santos Tech")

-- name: GetOAuthClientByClientID :one
SELECT id::text, client_id, name, redirect_uris, is_active, created_at
FROM oauth_clients WHERE client_id = $1;

-- name: ListOAuthClients :many
SELECT id::text, client_id, name, redirect_uris, is_active, created_at
FROM oauth_clients ORDER BY created_at DESC;

-- name: InsertOAuthClient :one
INSERT INTO oauth_clients (client_id, name, redirect_uris)
VALUES ($1, $2, $3)
RETURNING id::text, client_id, name, redirect_uris, is_active, created_at;

-- name: UpdateOAuthClient :one
UPDATE oauth_clients SET
  name          = COALESCE($2, name),
  redirect_uris = COALESCE($3, redirect_uris),
  is_active     = COALESCE($4, is_active)
WHERE id = $1::uuid
RETURNING id::text, client_id, name, redirect_uris, is_active, created_at;

-- name: DeleteOAuthClient :execrows
DELETE FROM oauth_clients WHERE id = $1::uuid;
