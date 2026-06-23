-- Queries de IP Bans (admin CRUD)

-- name: UpsertIPBan :one
INSERT INTO ip_bans (ip, reason, banned_by, expires_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (ip) DO UPDATE
SET reason     = EXCLUDED.reason,
    banned_by  = EXCLUDED.banned_by,
    expires_at = EXCLUDED.expires_at,
    created_at = now()
RETURNING id, ip, reason, banned_by, expires_at, created_at;

-- name: GetIPBan :one
SELECT id, ip, reason, banned_by, expires_at, created_at
FROM ip_bans WHERE id = $1;

-- name: ListActiveBans :many
SELECT id, ip, reason, banned_by, expires_at, created_at
FROM ip_bans
WHERE expires_at IS NULL OR expires_at > now()
ORDER BY created_at DESC;

-- name: DeleteIPBan :execrows
DELETE FROM ip_bans WHERE id = $1;
