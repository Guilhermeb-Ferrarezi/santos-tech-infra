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

// migrate adiciona apenas o que é novo (MFA, preferências). As tabelas base já existem (Drizzle).
const migration = `
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS preferences JSONB NOT NULL DEFAULT '{}';
ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_bytes BIGINT NOT NULL DEFAULT 524288000;
CREATE TABLE IF NOT EXISTS recovery_codes (
  id         BIGSERIAL PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash  TEXT NOT NULL,
  used_at    TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_recovery_codes_user ON recovery_codes(user_id);
CREATE TABLE IF NOT EXISTS api_keys (
  id           BIGSERIAL PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  key_prefix   TEXT NOT NULL,
  key_hash     TEXT NOT NULL UNIQUE,
  last_used_at TIMESTAMPTZ,
  expires_at   TIMESTAMPTZ,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys(user_id);
`

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, migration)
	return err
}

// uuid colunas vêm com ::text pra escanear direto em string.
const userCols = `id, email, username, name, password_hash, avatar_url, role, custom_role_id::text, mfa_enabled, totp_secret, suspended_at, created_at, preferences`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Username, &u.Name, &u.PasswordHash, &u.AvatarURL,
		&u.Role, &u.CustomRoleID, &u.MFAEnabled, &u.TOTPSecret, &u.SuspendedAt, &u.CreatedAt, &u.Preferences)
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

// Domínio das contas internas gerenciadas pela tela de Usuários do painel.
const staffDomain = "santos-tech.com"

// insertUserWithRole cria um usuário SEM senha (password_hash NULL) e com o role
// dado. Ele não consegue logar até definir a senha pelo convite (reset-password).
func (s *Server) insertUserWithRole(ctx context.Context, email, name string, role int16) (*User, error) {
	return scanUser(s.db.QueryRow(ctx,
		`INSERT INTO users (email, name, role) VALUES ($1,$2,$3) RETURNING `+userCols,
		email, name, role))
}

// listUsersByDomain devolve os usuários cujo email termina em @<domain>, do mais
// recente para o mais antigo.
func (s *Server) listUsersByDomain(ctx context.Context, domain string) ([]User, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+userCols+` FROM users WHERE email LIKE '%@' || $1 ORDER BY created_at DESC`,
		domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	return users, rows.Err()
}

// updateUserAdmin atualiza nome e/ou role (campos nil são ignorados via COALESCE).
func (s *Server) updateUserAdmin(ctx context.Context, id int64, name *string, role *int16) (*User, error) {
	return scanUser(s.db.QueryRow(ctx,
		`UPDATE users SET name = COALESCE($2, name), role = COALESCE($3, role)
		 WHERE id = $1 RETURNING `+userCols,
		id, name, role))
}

// setUserSuspended seta (now()) ou limpa (NULL) o suspended_at.
func (s *Server) setUserSuspended(ctx context.Context, id int64, suspended bool) error {
	_, err := s.db.Exec(ctx,
		`UPDATE users SET suspended_at = CASE WHEN $2 THEN now() ELSE NULL END WHERE id = $1`,
		id, suspended)
	return err
}

// deleteUser remove o usuário (cascata em recovery_codes/api_keys via FK).
func (s *Server) deleteUser(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	return err
}

func (s *Server) updatePassword(ctx context.Context, userID int64, hash string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id=$2`, hash, userID)
	return err
}

func (s *Server) updateAvatarURL(ctx context.Context, userID int64, url string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET avatar_url=$1 WHERE id=$2`, url, userID)
	return err
}

func (s *Server) setMFA(ctx context.Context, userID int64, enabled bool, secret *string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET mfa_enabled=$1, totp_secret=$2 WHERE id=$3`, enabled, secret, userID)
	return err
}

// upsertPreferences faz merge parcial do objeto JSON `patch` no JSONB users.preferences
// (chaves novas entram, existentes são sobrescritas) e devolve o resultado final.
func (s *Server) upsertPreferences(ctx context.Context, userID int64, patch []byte) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.db.QueryRow(ctx,
		`UPDATE users SET preferences = preferences || $1::jsonb WHERE id=$2 RETURNING preferences`,
		string(patch), userID).Scan(&out)
	return out, err
}

func (s *Server) buildProfile(ctx context.Context, u *User) *UserProfile {
	prefs := u.Preferences
	if len(prefs) == 0 {
		prefs = json.RawMessage("{}")
	}
	p := &UserProfile{
		ID: u.ID, Email: u.Email, Username: u.Username, Name: u.Name, Role: u.Role,
		CustomRoleID: u.CustomRoleID, AvatarURL: u.AvatarURL, MFAEnabled: u.MFAEnabled,
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339), Preferences: prefs,
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

// deleteExpiredSessions remove as sessões já vencidas. Sem isso a tabela cresce
// pra sempre (sessões abandonadas sem logout nunca saem), degradando sessionByHash.
func (s *Server) deleteExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	return tag.RowsAffected(), err
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

// ── API tokens (Personal Access Tokens) ──────────────────────────────────────

// APIKey é a visão segura de um token (sem o segredo) devolvida na listagem.
type APIKey struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	CreatedAt  time.Time  `json:"createdAt"`
}

func (s *Server) insertAPIKey(ctx context.Context, userID int64, name, prefix, hash string, expires *time.Time) (id int64, created time.Time, err error) {
	err = s.db.QueryRow(ctx,
		`INSERT INTO api_keys (user_id, name, key_prefix, key_hash, expires_at)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id, created_at`,
		userID, name, prefix, hash, expires).Scan(&id, &created)
	return
}

func (s *Server) listAPIKeys(ctx context.Context, userID int64) ([]APIKey, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, name, key_prefix, last_used_at, expires_at, created_at
		 FROM api_keys WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.LastUsedAt, &k.ExpiresAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Server) deleteAPIKey(ctx context.Context, userID, id int64) (bool, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM api_keys WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// userIDByAPIKeyHash valida um PAT pelo hash: devolve o user_id se o token existe e
// não expirou, marcando o último uso. (0, nil) significa token inválido/expirado.
func (s *Server) userIDByAPIKeyHash(ctx context.Context, hash string) (int64, error) {
	var userID int64
	err := s.db.QueryRow(ctx,
		`UPDATE api_keys SET last_used_at=now()
		 WHERE key_hash=$1 AND (expires_at IS NULL OR expires_at > now())
		 RETURNING user_id`, hash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return userID, err
}
