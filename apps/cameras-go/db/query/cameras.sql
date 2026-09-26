-- name: ListCameras :many
SELECT id, name, ip, rtsp_user, rtsp_password_encrypted, quality_main, quality_sub, record_mode, motion_sensitivity, is_active, created_at, updated_at FROM cameras ORDER BY name;

-- name: GetCamera :one
SELECT id, name, ip, rtsp_user, rtsp_password_encrypted, quality_main, quality_sub, record_mode, motion_sensitivity, is_active, created_at, updated_at FROM cameras WHERE id = $1;

-- name: CreateCamera :one
INSERT INTO cameras (
    name, ip, rtsp_user, rtsp_password_encrypted, quality_main, quality_sub, record_mode, motion_sensitivity, is_active
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
) RETURNING id, name, ip, rtsp_user, rtsp_password_encrypted, quality_main, quality_sub, record_mode, motion_sensitivity, is_active, created_at, updated_at;

-- name: UpdateCamera :one
UPDATE cameras SET
    name = $2,
    ip = $3,
    rtsp_user = $4,
    rtsp_password_encrypted = $5,
    quality_main = $6,
    quality_sub = $7,
    record_mode = $8,
    motion_sensitivity = $9,
    is_active = $10,
    updated_at = NOW()
WHERE id = $1
RETURNING id, name, ip, rtsp_user, rtsp_password_encrypted, quality_main, quality_sub, record_mode, motion_sensitivity, is_active, created_at, updated_at;

-- name: DeleteCamera :exec
DELETE FROM cameras WHERE id = $1;

