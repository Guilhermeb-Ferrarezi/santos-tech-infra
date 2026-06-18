-- name: UserRole :one
SELECT role FROM users WHERE id = $1;

-- name: UserIDByAPIKeyHash :one
UPDATE api_keys
SET last_used_at = now()
WHERE key_hash = $1 AND (expires_at IS NULL OR expires_at > now())
RETURNING user_id;
