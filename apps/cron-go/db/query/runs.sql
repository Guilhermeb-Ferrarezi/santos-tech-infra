-- name: CreateRun :one
INSERT INTO cron_runs (job_id, status, attempt) VALUES ($1,$2,$3) RETURNING *;

-- name: FinishRun :exec
UPDATE cron_runs SET status=$2, http_status=$3, response_excerpt=$4, error=$5,
    attempt=$6, finished_at=now()
WHERE id=$1;

-- name: ListRunsByJob :many
SELECT * FROM cron_runs WHERE job_id=$1 ORDER BY started_at DESC LIMIT $2;

-- name: HasRunningRun :one
SELECT EXISTS(
    SELECT 1 FROM cron_runs WHERE job_id=$1 AND status='running'
) AS running;
