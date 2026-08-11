-- Queries de modelos 3D (biblioteca admin-only no dashboard)

-- name: CreateModel3DFile :one
INSERT INTO model3d_file (filename, object_key, ext, content_type, size_bytes, uploaded_by, folder, thumbnail_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, filename, object_key, ext, content_type, size_bytes, uploaded_by, created_at, folder, pinned, thumbnail_key;

-- name: GetModel3DFile :one
SELECT id, filename, object_key, ext, content_type, size_bytes, uploaded_by, created_at, folder, pinned, thumbnail_key
FROM model3d_file WHERE id = $1;

-- name: ListModel3DFiles :many
SELECT id, filename, object_key, ext, content_type, size_bytes, uploaded_by, created_at, folder, pinned, thumbnail_key
FROM model3d_file
ORDER BY pinned DESC, created_at DESC;

-- name: UpdateModel3DFile :one
UPDATE model3d_file
SET filename = $2, folder = $3, pinned = $4
WHERE id = $1
RETURNING id, filename, object_key, ext, content_type, size_bytes, uploaded_by, created_at, folder, pinned, thumbnail_key;

-- name: DeleteModel3DFile :execrows
DELETE FROM model3d_file WHERE id = $1;
