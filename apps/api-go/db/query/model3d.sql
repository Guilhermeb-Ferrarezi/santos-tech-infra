-- Queries de modelos 3D (biblioteca admin-only no dashboard)

-- name: CreateModel3DFile :one
INSERT INTO model3d_file (filename, object_key, ext, content_type, size_bytes, uploaded_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, filename, object_key, ext, content_type, size_bytes, uploaded_by, created_at;

-- name: GetModel3DFile :one
SELECT id, filename, object_key, ext, content_type, size_bytes, uploaded_by, created_at
FROM model3d_file WHERE id = $1;

-- name: ListModel3DFiles :many
SELECT id, filename, object_key, ext, content_type, size_bytes, uploaded_by, created_at
FROM model3d_file
ORDER BY created_at DESC;

-- name: DeleteModel3DFile :execrows
DELETE FROM model3d_file WHERE id = $1;
