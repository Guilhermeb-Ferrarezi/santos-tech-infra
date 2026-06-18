-- name: SavePushToken :exec
INSERT INTO claude_push_tokens (token, user_id)
VALUES ($1, $2)
ON CONFLICT (token) DO UPDATE SET user_id = $2;

-- name: PushTokensForUser :many
SELECT token FROM claude_push_tokens WHERE user_id = $1;
