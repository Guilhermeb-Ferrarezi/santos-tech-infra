-- Fila/auditoria de ações remotas nos PCs do laboratório.

-- name: EnqueueLabDeviceCommand :one
INSERT INTO hour_lab_device_commands (device_id, user_id, source, kind, text)
VALUES (sqlc.arg(device_id)::uuid, sqlc.arg(user_id), sqlc.arg(source), 'command', sqlc.arg(text))
RETURNING id::text;

-- name: InsertLabDeviceAudit :exec
INSERT INTO hour_lab_device_commands (device_id, user_id, source, kind, text, delivered_at)
VALUES (sqlc.arg(device_id)::uuid, sqlc.arg(user_id), sqlc.arg(source), sqlc.arg(kind), sqlc.arg(text), now());

-- name: ClaimNextLabDeviceCommand :one
UPDATE hour_lab_device_commands c SET delivered_at = now()
WHERE c.id = (
  SELECT q.id FROM hour_lab_device_commands q
  JOIN hour_lab_devices d ON d.id = q.device_id
  WHERE d.device_uuid = sqlc.arg(device_uuid) AND q.kind = 'command' AND q.delivered_at IS NULL
  ORDER BY q.created_at
  LIMIT 1
  FOR UPDATE OF q SKIP LOCKED
)
RETURNING c.id::text, c.text;

-- name: StoreLabDeviceCommandQueueResult :execrows
UPDATE hour_lab_device_commands c SET result = sqlc.arg(result), result_at = now()
FROM hour_lab_devices d
WHERE d.id = c.device_id AND d.device_uuid = sqlc.arg(device_uuid)
  AND c.id = sqlc.arg(command_id)::uuid AND c.result_at IS NULL;

-- name: MirrorLabDeviceLegacyCommand :exec
-- Espelho nas colunas antigas até o front migrar pro /commands (Plano 2).
UPDATE hour_lab_devices
SET command_id = sqlc.arg(command_id)::uuid, command_text = sqlc.arg(text), command_sent_at = now(),
    command_result = NULL, command_result_at = NULL
WHERE id = sqlc.arg(device_id)::uuid;

-- name: ListLabDeviceCommands :many
SELECT c.id::text, c.kind, c.source, c.text, c.result, c.user_id,
       COALESCE(u.name, '')::text AS user_name, c.created_at, c.delivered_at, c.result_at
FROM hour_lab_device_commands c LEFT JOIN users u ON u.id = c.user_id
WHERE c.device_id = sqlc.arg(device_id)::uuid
ORDER BY c.created_at DESC
LIMIT sqlc.arg(lim);

-- name: GetLabDeviceCommand :one
SELECT c.id::text, c.kind, c.source, c.text, c.result, c.user_id,
       COALESCE(u.name, '')::text AS user_name, c.created_at, c.delivered_at, c.result_at
FROM hour_lab_device_commands c LEFT JOIN users u ON u.id = c.user_id
WHERE c.device_id = sqlc.arg(device_id)::uuid AND c.id = sqlc.arg(id)::uuid;
