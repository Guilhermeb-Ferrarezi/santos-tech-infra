-- Retrato das turmas (migration 0046). Ver turmas.go.

-- name: UpsertTurmaRetrato :exec
INSERT INTO bot_turma_retrato (tenant_id, turma_id, nome, curso, horarios, inicio, fim_previsto, alunos, capacidade, vagas, capturado_em, sumiu_em)
VALUES (sqlc.arg(tenant_id), sqlc.arg(turma_id), sqlc.arg(nome), sqlc.arg(curso), sqlc.arg(horarios), sqlc.arg(inicio)::date, sqlc.arg(fim_previsto)::date,
        sqlc.arg(alunos), sqlc.arg(capacidade), sqlc.arg(vagas), sqlc.arg(capturado_em), NULL)
ON CONFLICT (tenant_id, turma_id) DO UPDATE SET
  nome = EXCLUDED.nome, curso = EXCLUDED.curso, horarios = EXCLUDED.horarios,
  inicio = EXCLUDED.inicio, fim_previsto = EXCLUDED.fim_previsto,
  alunos = EXCLUDED.alunos, capacidade = EXCLUDED.capacidade, vagas = EXCLUDED.vagas,
  capturado_em = EXCLUDED.capturado_em, sumiu_em = NULL;

-- name: MarcaTurmasSumidas :exec
-- presentes: ids separados por vírgula ('' = nenhuma veio). Texto em vez de
-- array para o código gerado (database/sql) não depender de lib/pq.
UPDATE bot_turma_retrato SET sumiu_em = sqlc.arg(agora)::timestamptz
WHERE tenant_id = sqlc.arg(tenant_id) AND sumiu_em IS NULL
  AND NOT (turma_id = ANY(string_to_array(sqlc.arg(presentes)::text, ',')::bigint[]));

-- name: ListaTurmasRetrato :many
SELECT turma_id, nome, curso, horarios, inicio::text AS inicio, fim_previsto::text AS fim_previsto,
       alunos, capacidade, vagas, capturado_em, sumiu_em
FROM bot_turma_retrato
WHERE tenant_id = sqlc.arg(tenant_id)
ORDER BY turma_id;
