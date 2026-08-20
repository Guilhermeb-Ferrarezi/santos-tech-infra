-- name: CreateRun :one
INSERT INTO cron_runs (job_id, status, attempt) VALUES ($1,$2,$3) RETURNING *;

-- name: FinishRun :exec
UPDATE cron_runs SET status=$2, http_status=$3, response_excerpt=$4, error=$5,
    attempt=$6, finished_at=now()
WHERE id=$1;

-- name: ListRunsByJob :many
SELECT * FROM cron_runs WHERE job_id=$1 ORDER BY started_at DESC LIMIT $2;

-- name: HasRunningRun :one
-- Janela de 1h cura runs órfãos de dispatch interrompido (SIGTERM/crash):
-- sem o filtro de staleness, um run travado em 'running' bloquearia o job para sempre.
SELECT EXISTS(
    SELECT 1 FROM cron_runs
    WHERE job_id=$1 AND status='running'
      AND started_at > now() - interval '1 hour'
) AS running;

-- name: LockJobForRun :exec
-- Lock consultivo com escopo de transação, por job. Serializa "checar
-- sobreposição" + "criar run": sem ele a checagem e a inserção eram dois
-- statements soltos, e o tick do scheduler concorrendo com um disparo manual
-- (ou duas réplicas) criava dois runs para o mesmo job.
SELECT pg_advisory_xact_lock($1);

-- name: PurgeOldRuns :execrows
-- Retenção do histórico. O scheduler tica a cada 30s e cada execução grava uma
-- linha com response_excerpt TEXT: um job de 1/min gera ~525 mil linhas por
-- ano, e nada apagava nada. Apaga só o que já terminou (status != 'running'),
-- para nunca remover um run em andamento.
DELETE FROM cron_runs
WHERE id IN (
    SELECT id FROM cron_runs
    WHERE status <> 'running'
      AND started_at < now() - (sqlc.arg(retention_days)::int * interval '1 day')
    ORDER BY started_at
    LIMIT sqlc.arg(max_rows)
);
