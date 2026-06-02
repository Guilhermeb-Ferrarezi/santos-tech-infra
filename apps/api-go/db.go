package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newDB(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(c); err != nil {
		return nil, err
	}
	return pool, nil
}

// migrate adiciona apenas o que é novo (MFA). As tabelas base já existem (Drizzle).
const migration = `
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret TEXT;
CREATE TABLE IF NOT EXISTS recovery_codes (
  id         BIGSERIAL PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash  TEXT NOT NULL,
  used_at    TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_recovery_codes_user ON recovery_codes(user_id);
`

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, migration)
	return err
}

// uuid colunas vêm com ::text pra escanear direto em string.
const userCols = `id, email, username, name, password_hash, avatar_url, role, custom_role_id::text, mfa_enabled, totp_secret, suspended_at, created_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Username, &u.Name, &u.PasswordHash, &u.AvatarURL,
		&u.Role, &u.CustomRoleID, &u.MFAEnabled, &u.TOTPSecret, &u.SuspendedAt, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

func (s *Server) userByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id))
}

func (s *Server) userByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email=$1`, email))
}

func (s *Server) userByIdentifier(ctx context.Context, identifier string) (*User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email=$1 OR username=$1`, identifier))
}

func (s *Server) insertUser(ctx context.Context, email, name, passwordHash string) (*User, error) {
	return scanUser(s.db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1,$2,$3) RETURNING `+userCols,
		email, name, passwordHash))
}

func (s *Server) updatePassword(ctx context.Context, userID int64, hash string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id=$2`, hash, userID)
	return err
}

func (s *Server) setMFA(ctx context.Context, userID int64, enabled bool, secret *string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET mfa_enabled=$1, totp_secret=$2 WHERE id=$3`, enabled, secret, userID)
	return err
}

func (s *Server) buildProfile(ctx context.Context, u *User) *UserProfile {
	p := &UserProfile{
		ID: u.ID, Email: u.Email, Username: u.Username, Name: u.Name, Role: u.Role,
		CustomRoleID: u.CustomRoleID, AvatarURL: u.AvatarURL, MFAEnabled: u.MFAEnabled,
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339),
	}
	if u.SuspendedAt != nil {
		v := u.SuspendedAt.UTC().Format(time.RFC3339)
		p.SuspendedAt = &v
	}
	if u.Role == RoleCustom && u.CustomRoleID != nil {
		var raw []byte
		if err := s.db.QueryRow(ctx, `SELECT permissions FROM custom_roles WHERE id=$1`, *u.CustomRoleID).Scan(&raw); err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &p.Permissions)
		}
	}
	return p
}

// ── Sessões (refresh tokens) ─────────────────────────────────────────────────

func (s *Server) createSession(ctx context.Context, userID int64, refreshHash string, expires time.Time) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO sessions (user_id, refresh_token_hash, expires_at) VALUES ($1,$2,$3)`,
		userID, refreshHash, expires)
	return err
}

func (s *Server) sessionByHash(ctx context.Context, hash string) (sessionID string, userID int64, expires time.Time, err error) {
	err = s.db.QueryRow(ctx,
		`SELECT id::text, user_id, expires_at FROM sessions WHERE refresh_token_hash=$1`, hash).
		Scan(&sessionID, &userID, &expires)
	return
}

func (s *Server) deleteSession(ctx context.Context, sessionID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE id=$1`, sessionID)
	return err
}

func (s *Server) deleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID)
	return err
}

// ── OAuth ────────────────────────────────────────────────────────────────────

func (s *Server) linkOAuth(ctx context.Context, userID int64, provider, providerID string) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO oauth_accounts (user_id, provider, provider_id) VALUES ($1,$2,$3)
		 ON CONFLICT (provider, provider_id) DO NOTHING`,
		userID, provider, providerID)
	return err
}
