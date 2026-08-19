-- Queries do catálogo de downloads (Santos Hub — app Tauri dos PCs da empresa)

-- name: CreateDownload :one
INSERT INTO downloads (name, description, category, version, kind, object_key, external_url, filename, content_type, size_bytes, uploaded_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING id, name, description, category, version, kind, object_key, external_url, filename, content_type, size_bytes, pinned, uploaded_by, created_at, updated_at;

-- name: GetDownload :one
SELECT id, name, description, category, version, kind, object_key, external_url, filename, content_type, size_bytes, pinned, uploaded_by, created_at, updated_at
FROM downloads WHERE id = $1;

-- name: ListDownloads :many
SELECT id, name, description, category, version, kind, object_key, external_url, filename, content_type, size_bytes, pinned, uploaded_by, created_at, updated_at
FROM downloads
ORDER BY pinned DESC, created_at DESC;

-- name: UpdateDownload :one
UPDATE downloads
SET name = $2, description = $3, category = $4, version = $5, pinned = $6, updated_at = now()
WHERE id = $1
RETURNING id, name, description, category, version, kind, object_key, external_url, filename, content_type, size_bytes, pinned, uploaded_by, created_at, updated_at;

-- name: DeleteDownload :execrows
DELETE FROM downloads WHERE id = $1;
