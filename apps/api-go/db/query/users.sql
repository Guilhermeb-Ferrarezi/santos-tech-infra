-- Queries de Custom Roles (admin CRUD)

-- name: ListCustomRoles :many
SELECT id::text, name, description, permissions, created_at, updated_at
FROM custom_roles ORDER BY created_at DESC;

-- name: CreateCustomRole :one
INSERT INTO custom_roles (name, description, permissions)
VALUES ($1, $2, $3::jsonb)
RETURNING id::text, name, description, permissions, created_at, updated_at;

-- name: GetCustomRole :one
SELECT id::text, name, description, permissions, created_at, updated_at
FROM custom_roles WHERE id = $1::uuid;

-- name: UpdateCustomRole :one
UPDATE custom_roles
SET name = $2, description = $3, permissions = $4::jsonb, updated_at = now()
WHERE id = $1::uuid
RETURNING id::text, name, description, permissions, created_at, updated_at;

-- name: CountUsersWithCustomRole :one
SELECT COUNT(*) FROM users WHERE custom_role_id = $1::uuid;

-- name: DeleteCustomRole :execrows
DELETE FROM custom_roles WHERE id = $1::uuid;
