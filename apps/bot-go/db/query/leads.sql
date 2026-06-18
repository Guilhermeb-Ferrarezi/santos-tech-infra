-- name: LeadCreate :one
INSERT INTO lead (tenant_id, contact_id, status)
VALUES ($1, $2, 'novo')
ON CONFLICT (tenant_id, contact_id) DO UPDATE SET status = lead.status
RETURNING id, created_at;

-- name: LeadCreateWithOrigin :exec
INSERT INTO lead (tenant_id, contact_id, status, origin)
VALUES ($1, $2, 'novo', $3)
ON CONFLICT (tenant_id, contact_id) DO NOTHING;

-- name: LeadPatchStatus :execrows
UPDATE lead
SET status     = COALESCE($3, status),
    owner      = COALESCE($4, owner),
    interest   = COALESCE($5, interest),
    updated_at = now()
WHERE tenant_id = $1 AND id = $2;

-- name: LeadDelete :execrows
DELETE FROM lead WHERE tenant_id = $1 AND id = $2;
