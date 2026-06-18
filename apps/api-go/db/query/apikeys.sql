-- Queries de API Keys (Personal Access Tokens)

-- name: InsertAPIKey :one
INSERT INTO api_keys (user_id, name, key_prefix, key_hash, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, created_at;

-- name: ListAPIKeys :many
SELECT id, name, key_prefix, last_used_at, expires_at, created_at
FROM api_keys WHERE user_id = $1 ORDER BY created_at DESC;

-- name: DeleteAPIKey :execrows
DELETE FROM api_keys WHERE id = $1 AND user_id = $2;

-- name: ValidateAndTouchAPIKey :one
UPDATE api_keys SET last_used_at = now()
WHERE key_hash = $1
  AND (expires_at IS NULL OR expires_at > now())
  AND EXISTS (
    SELECT 1 FROM users u
    WHERE u.id = api_keys.user_id
      AND u.suspended_at IS NULL
      AND NOT u.login_disabled
  )
RETURNING user_id;
