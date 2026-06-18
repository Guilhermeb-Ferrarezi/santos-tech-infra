-- Queries de autenticação: usuários e sessões

-- name: GetUserByID :one
SELECT id, email, username, name, password_hash, avatar_url, role, custom_role_id::text, mfa_enabled, totp_secret, suspended_at, created_at, preferences, quota_bytes, email_verified_at, mfa_method, login_disabled
FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT id, email, username, name, password_hash, avatar_url, role, custom_role_id::text, mfa_enabled, totp_secret, suspended_at, created_at, preferences, quota_bytes, email_verified_at, mfa_method, login_disabled
FROM users WHERE email = $1;

-- name: GetUserByIdentifier :one
SELECT id, email, username, name, password_hash, avatar_url, role, custom_role_id::text, mfa_enabled, totp_secret, suspended_at, created_at, preferences, quota_bytes, email_verified_at, mfa_method, login_disabled
FROM users WHERE email = $1 OR username = $1;

-- name: InsertUser :one
INSERT INTO users (email, name, password_hash)
VALUES ($1, $2, $3)
RETURNING id, email, username, name, password_hash, avatar_url, role, custom_role_id::text, mfa_enabled, totp_secret, suspended_at, created_at, preferences, quota_bytes, email_verified_at, mfa_method, login_disabled;

-- name: InsertUserWithRole :one
INSERT INTO users (email, name, role)
VALUES ($1, $2, $3)
RETURNING id, email, username, name, password_hash, avatar_url, role, custom_role_id::text, mfa_enabled, totp_secret, suspended_at, created_at, preferences, quota_bytes, email_verified_at, mfa_method, login_disabled;

-- name: InsertSharedMailbox :one
INSERT INTO users (email, name, role, login_disabled)
VALUES ($1, $2, $3, true)
RETURNING id, email, username, name, password_hash, avatar_url, role, custom_role_id::text, mfa_enabled, totp_secret, suspended_at, created_at, preferences, quota_bytes, email_verified_at, mfa_method, login_disabled;

-- name: ListUsersByDomain :many
SELECT id, email, username, name, password_hash, avatar_url, role, custom_role_id::text, mfa_enabled, totp_secret, suspended_at, created_at, preferences, quota_bytes, email_verified_at, mfa_method, login_disabled
FROM users WHERE email LIKE '%@' || $1 ORDER BY created_at DESC;

-- name: ListAllUsers :many
SELECT id, email, username, name, password_hash, avatar_url, role, custom_role_id::text, mfa_enabled, totp_secret, suspended_at, created_at, preferences, quota_bytes, email_verified_at, mfa_method, login_disabled
FROM users ORDER BY created_at DESC;

-- name: UpdateUserAdmin :one
UPDATE users SET
  name           = COALESCE($2, name),
  role           = COALESCE($3, role),
  quota_bytes    = COALESCE($4, quota_bytes),
  custom_role_id = CASE
                     WHEN $3 = 4 THEN $5::uuid
                     WHEN $3 IS NOT NULL AND $3 != 4 THEN NULL
                     ELSE custom_role_id
                   END
WHERE id = $1
RETURNING id, email, username, name, password_hash, avatar_url, role, custom_role_id::text, mfa_enabled, totp_secret, suspended_at, created_at, preferences, quota_bytes, email_verified_at, mfa_method, login_disabled;

-- name: UnsuspendUser :exec
UPDATE users SET suspended_at = NULL WHERE id = $1;

-- name: SuspendUser :exec
UPDATE users SET suspended_at = now() WHERE id = $1;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: ExpireUserAPIKeys :exec
UPDATE api_keys SET expires_at = now()
WHERE user_id = $1 AND (expires_at IS NULL OR expires_at > now());

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;

-- name: UpdatePassword :exec
UPDATE users SET password_hash = $1 WHERE id = $2;

-- name: UpdateAvatarURL :exec
UPDATE users SET avatar_url = $1 WHERE id = $2;

-- name: SetMFA :exec
UPDATE users SET mfa_enabled = $1, totp_secret = $2 WHERE id = $3;

-- name: UpsertPreferences :one
UPDATE users SET preferences = preferences || $1::jsonb WHERE id = $2
RETURNING preferences;

-- name: GetCustomRolePermissions :one
SELECT permissions FROM custom_roles WHERE id = $1;

-- name: SetEmailVerified :exec
UPDATE users SET email_verified_at = now() WHERE id = $1;

-- name: SetMFAEnabled :exec
UPDATE users SET mfa_enabled = true, mfa_method = $1 WHERE id = $2;

-- name: SetMFAMethod :exec
UPDATE users SET mfa_method = $1 WHERE id = $2;

-- name: CreateSession :one
INSERT INTO sessions (user_id, refresh_token_hash, expires_at)
VALUES ($1, $2, $3)
RETURNING id::text;

-- name: GetSessionByHash :one
SELECT id::text, user_id, expires_at FROM sessions WHERE refresh_token_hash = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at < now();

-- name: DeleteSessionsByUser :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: GetSessionUser :one
SELECT u.id, u.email, u.username, u.name, u.password_hash, u.avatar_url, u.role, u.custom_role_id::text, u.mfa_enabled, u.totp_secret, u.suspended_at, u.created_at, u.preferences, u.quota_bytes, u.email_verified_at, u.mfa_method, u.login_disabled
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.id = $1::uuid AND s.expires_at > now();

-- name: GetAccountSummaries :many
SELECT s.id::text, u.name, u.email, u.avatar_url
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.id = ANY($1::uuid[]) AND s.expires_at > now() AND u.suspended_at IS NULL;

-- name: LinkOAuth :exec
INSERT INTO oauth_accounts (user_id, provider, provider_id)
VALUES ($1, $2, $3)
ON CONFLICT (provider, provider_id) DO NOTHING;
