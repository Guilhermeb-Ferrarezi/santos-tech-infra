-- name: CreateJob :one
INSERT INTO cron_jobs (name, description, schedule_cron, timezone, enabled,
    action_kind, action_ref, http_method, http_url, http_headers, http_body,
    params, timeout_secs, max_retries, next_run_at, created_by)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
RETURNING *;

-- name: GetJob :one
SELECT * FROM cron_jobs WHERE id = $1;

-- name: ListJobs :many
SELECT * FROM cron_jobs ORDER BY created_at DESC;

-- name: UpdateJob :one
UPDATE cron_jobs SET
    name=$2, description=$3, schedule_cron=$4, timezone=$5,
    action_kind=$6, action_ref=$7, http_method=$8, http_url=$9,
    http_headers=$10, http_body=$11, params=$12, timeout_secs=$13,
    max_retries=$14, next_run_at=$15, updated_at=now()
WHERE id=$1
RETURNING *;

-- name: SetJobEnabled :exec
UPDATE cron_jobs SET enabled=$2, next_run_at=$3, updated_at=now() WHERE id=$1;

-- name: DeleteJob :exec
DELETE FROM cron_jobs WHERE id=$1;

-- name: ClaimDueJobs :many
SELECT * FROM cron_jobs
WHERE enabled = TRUE AND next_run_at IS NOT NULL AND next_run_at <= now()
ORDER BY next_run_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: UpdateJobAfterRun :exec
UPDATE cron_jobs SET last_run_at=now(), next_run_at=$2, updated_at=now() WHERE id=$1;
