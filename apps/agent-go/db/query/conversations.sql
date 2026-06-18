-- name: InsertConversation :exec
INSERT INTO claude_conversations (id, user_id, title, repo, workdir, model, status, session_id, session_started, tools_disabled, effort, web_search)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: GetConversation :one
SELECT id::text, user_id, title, repo, workdir, model, status, session_id::text, session_started, tools_disabled, effort, web_search, created_at, updated_at
FROM claude_conversations
WHERE id = $1::uuid AND user_id = $2;

-- name: ListConversations :many
SELECT id::text, user_id, title, repo, workdir, model, status, session_id::text, session_started, tools_disabled, effort, web_search, created_at, updated_at
FROM claude_conversations
WHERE user_id = $1
ORDER BY updated_at DESC;

-- name: DeleteConversation :execrows
DELETE FROM claude_conversations
WHERE id = $1::uuid AND user_id = $2;

-- name: UpdateConversationModel :exec
UPDATE claude_conversations
SET model = $1, updated_at = now()
WHERE id = $2::uuid AND user_id = $3;

-- name: UpdateConversationTitle :exec
UPDATE claude_conversations
SET title = $1, updated_at = now()
WHERE id = $2::uuid AND user_id = $3;

-- name: SetTitleIfEmpty :exec
UPDATE claude_conversations
SET title = $1, updated_at = now()
WHERE id = $2::uuid AND (title IS NULL OR title = '');

-- name: SetConversationStatus :exec
UPDATE claude_conversations
SET status = $1, updated_at = now()
WHERE id = $2::uuid;

-- name: MarkSessionStarted :exec
UPDATE claude_conversations
SET session_started = true, updated_at = now()
WHERE id = $1::uuid;

-- name: RotateSession :exec
UPDATE claude_conversations
SET session_id = $1::uuid, session_started = false, updated_at = now()
WHERE id = $2::uuid AND user_id = $3;
