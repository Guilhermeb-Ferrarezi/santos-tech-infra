-- Contatos de aviso da conta (avisos.go).

-- name: GetUserAvisos :one
SELECT email, aviso_email, aviso_telefone
FROM users WHERE id = $1;

-- name: UpdateUserAvisos :one
-- set_* = false preserva a coluna; true grava o valor (NULL volta ao padrão).
-- Sem updated_at: a tabela users de produção não tem essa coluna.
UPDATE users SET
  aviso_email    = CASE WHEN sqlc.arg(set_email)::bool    THEN sqlc.narg(aviso_email)::text    ELSE aviso_email END,
  aviso_telefone = CASE WHEN sqlc.arg(set_telefone)::bool THEN sqlc.narg(aviso_telefone)::text ELSE aviso_telefone END
WHERE id = sqlc.arg(id)
RETURNING email, aviso_email, aviso_telefone;
