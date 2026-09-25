-- name: GetSettings :one
SELECT * FROM settings WHERE id = 1 LIMIT 1;

-- name: UpsertSettings :one
INSERT INTO settings (
    id, storage_quota_bytes, drive_refresh_token_encrypted, updated_at
) VALUES (
    1, $1, $2, NOW()
)
ON CONFLICT (id) DO UPDATE SET
    storage_quota_bytes = EXCLUDED.storage_quota_bytes,
    drive_refresh_token_encrypted = EXCLUDED.drive_refresh_token_encrypted,
    updated_at = NOW()
RETURNING *;
