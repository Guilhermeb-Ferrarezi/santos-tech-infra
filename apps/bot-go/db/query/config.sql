-- name: TenantConfigGet :one
SELECT tc.bot_name,
       tc.bot_gender,
       false                          AS reveal_ai_if_asked,
       tc.kb_content::text            AS kb_content,
       tc.system_prompt,
       tc.admin_system_prompt,
       t.timezone,
       tc.quiet_hours->>'start'       AS quiet_hours_start,
       tc.quiet_hours->>'end'         AS quiet_hours_end,
       tc.bot_enabled_by_default,
       tc.bot_allowed_numbers,
       tc.admin_whatsapp_number,
       tc.admin_whatsapp_numbers
FROM tenant_config tc
JOIN tenants t ON t.id = tc.tenant_id
WHERE tc.tenant_id = $1;

-- name: TenantConfigAppendKB :exec
UPDATE tenant_config
SET kb_content = COALESCE(kb_content, '[]'::jsonb) || $2::jsonb
WHERE tenant_id = $1;

-- name: TenantConfigGetFull :one
SELECT tc.bot_name,
       tc.bot_gender,
       tc.bot_enabled_by_default,
       tc.bot_allowed_numbers,
       tc.quiet_hours->>'start'           AS quiet_hours_start,
       tc.quiet_hours->>'end'             AS quiet_hours_end,
       tc.kb_content::text                AS kb_content,
       tc.system_prompt,
       tc.admin_system_prompt,
       tc.admin_whatsapp_numbers,
       tc.debounce_ms,
       tc.evolution_bot_reply_enabled,
       tc.evolution_lead_capture_enabled,
       tc.evolution_capture_disabled,
       tc.notif_phone,
       tc.notif_instance,
       tc.notif_enabled,
       tc.notif_on_success,
       tc.notif_on_container_down
FROM tenant_config tc
WHERE tc.tenant_id = $1;

-- name: TenantConfigGetBotNameGender :one
SELECT bot_name, bot_gender FROM tenant_config WHERE tenant_id = $1;

-- name: TenantConfigGetDebounce :one
SELECT debounce_ms FROM tenant_config WHERE tenant_id = $1;

-- name: TenantConfigGetBotReplyEnabled :one
SELECT evolution_bot_reply_enabled FROM tenant_config WHERE tenant_id = $1;

-- name: TenantConfigGetCaptureDisabled :one
SELECT evolution_capture_disabled FROM tenant_config WHERE tenant_id = $1;

-- name: TenantConfigGetAdmins :one
SELECT admin_whatsapp_number, admin_whatsapp_numbers FROM tenant_config WHERE tenant_id = $1;

-- name: TenantConfigGetNotif :one
SELECT notif_phone, notif_instance, notif_enabled, notif_on_success, notif_on_container_down
FROM tenant_config WHERE tenant_id = $1;

-- name: TenantConfigPatch :exec
UPDATE tenant_config
SET bot_name                        = $1,
    bot_gender                      = $2,
    bot_enabled_by_default          = $3,
    bot_allowed_numbers             = $4::jsonb,
    quiet_hours                     = CASE WHEN $5::text IS NULL THEN '{}'::jsonb ELSE $5::jsonb END,
    kb_content                      = $6::jsonb,
    system_prompt                   = $8,
    admin_whatsapp_number           = $9,
    admin_whatsapp_numbers          = $10::jsonb,
    debounce_ms                     = $11,
    admin_system_prompt             = $12,
    evolution_bot_reply_enabled     = $13,
    evolution_lead_capture_enabled  = $14,
    evolution_capture_disabled      = $15::jsonb,
    notif_phone                     = $16,
    notif_instance                  = $17,
    notif_enabled                   = $18,
    notif_on_success                = $19,
    notif_on_container_down         = $20
WHERE tenant_id = $7;
