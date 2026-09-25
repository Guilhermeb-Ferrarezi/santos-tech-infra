-- name: GetActiveDesignShare :one
SELECT token, created_at
FROM claude_design_shares
WHERE conversation_id = $1::uuid AND revoked_at IS NULL
LIMIT 1;

-- name: InsertDesignShare :exec
INSERT INTO claude_design_shares (token, conversation_id, created_by)
VALUES ($1, $2::uuid, $3);

-- name: RevokeDesignShares :execrows
UPDATE claude_design_shares
SET revoked_at = now()
WHERE conversation_id = $1::uuid AND revoked_at IS NULL;

-- name: GetSharedDesignWorkdir :one
SELECT c.workdir
FROM claude_design_shares s
JOIN claude_conversations c ON c.id = s.conversation_id
WHERE s.token = $1 AND s.revoked_at IS NULL AND c.kind = 'design';
