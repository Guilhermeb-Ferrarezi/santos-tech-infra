-- name: ContactFindByChannelIdentity :one
SELECT c.id, c.tenant_id, c.display_name, c.communication_style, c.created_at, c.updated_at,
       ci.id AS ci_id, ci.tenant_id AS ci_tenant_id, ci.contact_id AS ci_contact_id,
       ci.channel::text AS ci_channel, ci.external_id AS ci_external_id, ci.created_at AS ci_created_at
FROM contact c
JOIN channel_identity ci ON ci.contact_id = c.id AND ci.tenant_id = c.tenant_id
WHERE ci.channel = $1::channel
  AND ci.external_id = $2;

-- name: ContactFindByPhone :one
SELECT c.id, c.tenant_id, c.display_name, c.communication_style, c.created_at, c.updated_at
FROM contact c
JOIN channel_identity ci ON ci.contact_id = c.id AND ci.tenant_id = c.tenant_id
WHERE c.tenant_id = $1
  AND regexp_replace(ci.external_id, '\D', '', 'g') = $2
ORDER BY c.created_at ASC
LIMIT 1;

-- name: ContactInsert :one
INSERT INTO contact (tenant_id, display_name)
VALUES ($1, $2)
RETURNING id, tenant_id, display_name, created_at, updated_at;

-- name: ChannelIdentityUpsert :one
INSERT INTO channel_identity (tenant_id, contact_id, channel, external_id, display_handle)
VALUES ($1, $2, $3::channel, $4, $5)
ON CONFLICT (tenant_id, channel, external_id) DO UPDATE
    SET display_handle = EXCLUDED.display_handle
RETURNING id, tenant_id, contact_id, channel::text AS channel, external_id, created_at;

-- name: ChannelIdentityFindFirst :one
SELECT id, tenant_id, contact_id, channel::text AS channel, external_id, created_at
FROM channel_identity
WHERE tenant_id = $1 AND contact_id = $2
ORDER BY created_at ASC
LIMIT 1;

-- name: ContactSetCommunicationStyle :exec
UPDATE contact SET communication_style = $1, updated_at = now()
WHERE tenant_id = $2 AND id = $3;

-- name: LeadSummaryUpsert :exec
INSERT INTO lead_summary (tenant_id, contact_id, summary)
VALUES ($1, $2, $3)
ON CONFLICT (tenant_id, contact_id) DO UPDATE SET summary = EXCLUDED.summary, updated_at = now();
