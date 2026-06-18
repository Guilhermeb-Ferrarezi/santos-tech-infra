-- Queries de Boards (quadros Excalidraw)

-- name: ListBoardsForUser :many
SELECT b.id::text, b.owner_id, u.name, b.title,
  CASE WHEN b.owner_id = $1 THEN 'owner' ELSE m.role END,
  EXISTS (SELECT 1 FROM board_members WHERE board_id = b.id),
  b.scene_version, b.created_at, b.updated_at
FROM boards b
JOIN users u ON u.id = b.owner_id
LEFT JOIN board_members m ON m.board_id = b.id AND m.user_id = $1
WHERE b.owner_id = $1 OR m.user_id IS NOT NULL
ORDER BY b.updated_at DESC;

-- name: GetBoardForUser :one
SELECT b.id::text, b.owner_id, u.name, b.title,
  CASE WHEN b.owner_id = $2 THEN 'owner' ELSE m.role END,
  EXISTS (SELECT 1 FROM board_members WHERE board_id = b.id),
  b.scene_version, b.created_at, b.updated_at, b.scene
FROM boards b
JOIN users u ON u.id = b.owner_id
LEFT JOIN board_members m ON m.board_id = b.id AND m.user_id = $2
WHERE b.id = $1::uuid AND (b.owner_id = $2 OR m.user_id IS NOT NULL);

-- name: GetBoardRoleForUser :one
SELECT CASE WHEN b.owner_id = $2 THEN 'owner' ELSE m.role END
FROM boards b
LEFT JOIN board_members m ON m.board_id = b.id AND m.user_id = $2
WHERE b.id = $1::uuid AND (b.owner_id = $2 OR m.user_id IS NOT NULL);

-- name: InsertBoard :one
INSERT INTO boards (owner_id, title)
VALUES ($1, $2)
RETURNING id::text, owner_id, title, scene_version, created_at, updated_at;

-- name: UpdateBoardScene :one
UPDATE boards SET
  scene         = $2::jsonb,
  scene_version = scene_version + 1,
  title         = COALESCE($4, title),
  updated_at    = now()
WHERE id = $1::uuid AND scene_version = $3
RETURNING scene_version;

-- name: UpdateBoardTitle :one
UPDATE boards SET title = $2, updated_at = now()
WHERE id = $1::uuid
RETURNING scene_version;

-- name: DeleteBoard :exec
DELETE FROM boards WHERE id = $1::uuid;

-- name: ListBoardMembers :many
SELECT m.user_id, u.name, u.email, m.role, m.added_at
FROM board_members m JOIN users u ON u.id = m.user_id
WHERE m.board_id = $1::uuid
ORDER BY m.added_at;

-- name: UpsertBoardMember :exec
INSERT INTO board_members (board_id, user_id, role)
VALUES ($1::uuid, $2, $3)
ON CONFLICT (board_id, user_id) DO UPDATE SET role = EXCLUDED.role;

-- name: DeleteBoardMember :exec
DELETE FROM board_members WHERE board_id = $1::uuid AND user_id = $2;
