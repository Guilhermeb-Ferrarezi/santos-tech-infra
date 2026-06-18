-- name: SaveOAuthToken :exec
UPDATE claude_credentials
SET oauth_token_enc = $1, status = 'logged_in', updated_at = now()
WHERE id = 1;

-- name: ClearOAuthToken :exec
UPDATE claude_credentials
SET oauth_token_enc = NULL, status = 'logged_out', updated_at = now()
WHERE id = 1;

-- name: GetOAuthStatus :one
SELECT status FROM claude_credentials WHERE id = 1;

-- name: GetOAuthToken :one
SELECT oauth_token_enc FROM claude_credentials WHERE id = 1;
