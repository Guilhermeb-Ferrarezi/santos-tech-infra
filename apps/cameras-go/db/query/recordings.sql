-- name: ListRecordings :many
SELECT * FROM recordings
WHERE camera_id = $1 AND start_time >= $2 AND start_time < $3
ORDER BY start_time ASC;

-- name: CreateRecording :one
INSERT INTO recordings (
    camera_id, start_time, end_time, size_bytes, drive_file_id, has_motion, keep_forever
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
) RETURNING *;

-- name: UpdateKeepForever :exec
UPDATE recordings SET keep_forever = $2 WHERE id = $1;

-- name: DeleteRecording :exec
DELETE FROM recordings WHERE id = $1;

-- name: GetRecording :one
SELECT * FROM recordings WHERE id = $1;

-- name: ListOldestRecordings :many
SELECT * FROM recordings
WHERE keep_forever = false
ORDER BY start_time ASC
LIMIT $1;
