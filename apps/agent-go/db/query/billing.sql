-- name: GetBilling :one
SELECT bot_uses_api_key, api_key_enc, api_key_hint, updated_at FROM claude_billing WHERE id = 1;

-- name: SaveBillingAPIKey :exec
UPDATE claude_billing
SET api_key_enc = $1, api_key_hint = $2, updated_at = now()
WHERE id = 1;

-- name: ClearBillingAPIKey :exec
-- Sem chave o bot não tem como usá-la: apagar também desliga a troca.
UPDATE claude_billing
SET api_key_enc = NULL, api_key_hint = '', bot_uses_api_key = false, updated_at = now()
WHERE id = 1;

-- name: SetBotUsesAPIKey :execrows
-- Ligar exige chave gravada (checado no mesmo UPDATE, sem corrida com o DELETE):
-- 0 linhas afetadas = tentou ligar sem chave.
UPDATE claude_billing
SET bot_uses_api_key = sqlc.arg(enabled)::boolean, updated_at = now()
WHERE id = 1 AND (NOT sqlc.arg(enabled)::boolean OR api_key_enc IS NOT NULL);
