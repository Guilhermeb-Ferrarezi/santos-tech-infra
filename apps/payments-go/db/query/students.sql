-- name: CreateStudent :one
INSERT INTO pay_students (name, tax_id, email, phone)
VALUES ($1, $2, $3, $4)
RETURNING id, name, tax_id, email, phone, created_at;

-- name: ListStudents :many
SELECT id, name, tax_id, email, phone, created_at
FROM pay_students
ORDER BY name;

-- name: GetStudent :one
SELECT id, name, tax_id, email, phone, created_at
FROM pay_students
WHERE id = $1;
