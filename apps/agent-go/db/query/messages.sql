-- name: InsertMessage :exec
INSERT INTO claude_messages (conversation_id, role, kind, content, usage)
VALUES ($1::uuid, $2, $3, $4, $5);

-- name: ListMessages :many
SELECT id, conversation_id::text, role, kind, content, usage, created_at
FROM claude_messages
WHERE conversation_id = $1::uuid
ORDER BY id;
