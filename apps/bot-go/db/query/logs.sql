-- name: ProcessingLogInsert :exec
INSERT INTO bot_processing_log
  (tenant_id, kind, conversation_id, contact_phone, contact_name, inbound_text,
   answered, answered_from_kb, handoff, cited_entry_ids, bubbles, tool_calls, processing_ms, error)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14);

-- name: ProcessingLogList :many
SELECT id::text                         AS id,
       COALESCE(kind, 'message')        AS kind,
       tenant_id,
       COALESCE(conversation_id::text, '') AS conversation_id,
       contact_phone,
       contact_name,
       inbound_text,
       answered,
       answered_from_kb,
       handoff,
       cited_entry_ids::text            AS cited_entry_ids,
       bubbles::text                    AS bubbles,
       COALESCE(tool_calls::text, 'null') AS tool_calls,
       processing_ms,
       COALESCE(error, '')              AS error,
       created_at
FROM bot_processing_log
WHERE tenant_id = $1 AND created_at < $2
ORDER BY created_at DESC
LIMIT $3;
