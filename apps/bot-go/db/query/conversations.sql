-- name: ConversationFindByChannelIdentity :one
SELECT id, tenant_id, channel_identity_id, channel::text AS channel, state::text AS state,
       bot_enabled, last_inbound_at, last_outbound_at, last_inbound_modality::text AS last_inbound_modality,
       created_at, updated_at
FROM conversation
WHERE channel_identity_id = $1;

-- name: ConversationFindByID :one
SELECT id, tenant_id, channel_identity_id, channel::text AS channel, state::text AS state,
       bot_enabled, last_inbound_at, last_outbound_at, last_inbound_modality::text AS last_inbound_modality,
       created_at, updated_at
FROM conversation
WHERE tenant_id = $1 AND id = $2;

-- name: ConversationCreate :one
INSERT INTO conversation (tenant_id, channel_identity_id, channel, state, bot_enabled)
VALUES ($1, $2, $3::channel, 'NEW', $4)
RETURNING id, tenant_id, channel_identity_id, channel::text AS channel, state::text AS state,
          bot_enabled, last_inbound_at, last_outbound_at,
          last_inbound_modality::text AS last_inbound_modality,
          created_at, updated_at;

-- name: ConversationSave :exec
UPDATE conversation
SET state                 = $1::conversation_state,
    last_inbound_at       = $2,
    last_outbound_at      = $3,
    last_inbound_modality = $4::reply_modality,
    updated_at            = now()
WHERE tenant_id = $5 AND id = $6;

-- name: ConversationSetBotEnabled :exec
UPDATE conversation SET bot_enabled = $1, updated_at = now()
WHERE tenant_id = $2 AND id = $3;

-- name: ConversationSetState :exec
UPDATE conversation SET state = $1::conversation_state, updated_at = now()
WHERE tenant_id = $2 AND id = $3;

-- name: ConversationDelete :exec
DELETE FROM conversation
WHERE tenant_id = $1 AND id = $2;

-- name: ConversationGetForSend :one
SELECT ci.external_id AS phone, conv.channel::text AS channel
FROM conversation conv
JOIN channel_identity ci ON ci.id = conv.channel_identity_id
WHERE conv.tenant_id = $1 AND conv.id = $2;

-- name: ConversationGetByPhone :one
SELECT conv.id::text AS id, conv.channel::text AS channel
FROM conversation conv
JOIN channel_identity ci ON ci.id = conv.channel_identity_id
WHERE conv.tenant_id = $1
  AND regexp_replace(ci.external_id, '\D', '', 'g') = $2
ORDER BY conv.last_inbound_at DESC NULLS LAST
LIMIT 1;
