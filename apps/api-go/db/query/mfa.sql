-- Queries de MFA: recovery codes

-- name: InsertRecoveryCodes :exec
INSERT INTO recovery_codes (user_id, code_hash)
SELECT $1, unnest($2::text[]);

-- name: ConsumeRecoveryCode :execrows
UPDATE recovery_codes SET used_at = now()
WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL;

-- name: DeleteRecoveryCodes :exec
DELETE FROM recovery_codes WHERE user_id = $1;
