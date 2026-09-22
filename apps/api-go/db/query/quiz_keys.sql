-- Queries das chaves de acesso da extensão do Jev (POST /quiz/answer sem
-- conta santos-tech) — ver quiz_keys.go.

-- name: GetQuizAccessKeyByHash :one
SELECT id, label, daily_limit, usage_date, usage_count, revoked_at
FROM quiz_access_keys WHERE key_hash = $1;

-- name: ReserveQuizKeyUsage :one
-- Reserva atômica de UMA unidade de cota: UPDATE condicional numa linha só,
-- sem SELECT prévio — o WHERE só casa se a chave não estiver revogada e
-- ainda houver cota HOJE (usage_date != hoje conta como zero, mesmo reset
-- diário de sempre). Duas chamadas concorrentes pra mesma chave serializam
-- no lock de linha do Postgres: a segunda só enxerga o usage_count já
-- incrementado pela primeira, então nunca reservam a mesma vaga. Sem linha
-- afetada (RETURNING vazio) = reserva NÃO concedida (cota esgotada, chave
-- revogada nesse meio-tempo, ou id inexistente) — ver reserveQuizKeyUsage em
-- quiz_keys.go.
UPDATE quiz_access_keys
   SET usage_count = CASE WHEN usage_date = (now() AT TIME ZONE 'utc')::date
                           THEN usage_count + 1 ELSE 1 END,
       usage_date  = (now() AT TIME ZONE 'utc')::date
 WHERE id = $1
   AND revoked_at IS NULL
   AND CASE WHEN usage_date = (now() AT TIME ZONE 'utc')::date
            THEN usage_count ELSE 0 END < daily_limit
RETURNING id;
