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

// migrate cria as tabelas próprias do agente. Idempotente, roda no boot.
// A tabela `users` já existe no Postgres compartilhado (do auth) e não é criada aqui.
const migration = `
CREATE TABLE IF NOT EXISTS claude_conversations (
  id              UUID PRIMARY KEY,
  user_id         BIGINT NOT NULL REFERENCES users(id),
  title           TEXT,
  repo            TEXT,
  workdir         TEXT NOT NULL,
  model           TEXT NOT NULL DEFAULT 'sonnet',
  status          TEXT NOT NULL DEFAULT 'idle',
  session_id      UUID NOT NULL,
  session_started BOOLEAN NOT NULL DEFAULT false,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_conv_user ON claude_conversations(user_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS claude_messages (
  id              BIGSERIAL PRIMARY KEY,
  conversation_id UUID NOT NULL REFERENCES claude_conversations(id) ON DELETE CASCADE,
  role            TEXT NOT NULL,
  kind            TEXT NOT NULL,
  content         JSONB NOT NULL,
  usage           JSONB,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_msg_conv ON claude_messages(conversation_id, id);

CREATE TABLE IF NOT EXISTS claude_credentials (
  id              INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  oauth_token_enc BYTEA,
  status          TEXT NOT NULL DEFAULT 'logged_out',
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO claude_credentials (id, status) VALUES (1, 'logged_out') ON CONFLICT (id) DO NOTHING;
`

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, migration)
	return err
}

// ── Papel do usuário (para o admin guard) ────────────────────────────────────

func (s *Server) userRole(ctx context.Context, userID int64) (int16, error) {
	var role int16
	err := s.db.QueryRow(ctx, `SELECT role FROM users WHERE id=$1`, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return role, err
}

// ── Conversas ────────────────────────────────────────────────────────────────

const convCols = `id::text, user_id, title, repo, workdir, model, status, session_id::text, session_started, created_at, updated_at`

func scanConversation(row pgx.Row) (*Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.UserID, &c.Title, &c.Repo, &c.Workdir, &c.Model, &c.Status,
		&c.SessionID, &c.SessionStarted, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

func (s *Server) insertConversation(ctx context.Context, c *Conversation) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO claude_conversations (id, user_id, title, repo, workdir, model, status, session_id, session_started)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		c.ID, c.UserID, c.Title, c.Repo, c.Workdir, c.Model, c.Status, c.SessionID, c.SessionStarted)
	return err
}

func (s *Server) conversationByID(ctx context.Context, id string, userID int64) (*Conversation, error) {
	return scanConversation(s.db.QueryRow(ctx,
		`SELECT `+convCols+` FROM claude_conversations WHERE id=$1 AND user_id=$2`, id, userID))
}

func (s *Server) listConversations(ctx context.Context, userID int64) ([]Conversation, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+convCols+` FROM claude_conversations WHERE user_id=$1 ORDER BY updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Server) deleteConversation(ctx context.Context, id string, userID int64) (bool, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM claude_conversations WHERE id=$1 AND user_id=$2`, id, userID)
	return tag.RowsAffected() > 0, err
}

func (s *Server) updateConversationModel(ctx context.Context, id string, userID int64, model string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE claude_conversations SET model=$1, updated_at=now() WHERE id=$2 AND user_id=$3`,
		model, id, userID)
	return err
}

func (s *Server) setConversationStatus(ctx context.Context, id, status string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE claude_conversations SET status=$1, updated_at=now() WHERE id=$2`, status, id)
	return err
}

// markSessionStarted marca que o 1º turno da sessão atual já rodou (próximos usam --resume).
func (s *Server) markSessionStarted(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE claude_conversations SET session_started=true, updated_at=now() WHERE id=$1`, id)
	return err
}

// rotateSession gera um novo claude session_id e zera o "started" — contexto novo.
// Preserva o id (PK) e as mensagens. Usado por /clear e /compact.
func (s *Server) rotateSession(ctx context.Context, id string, userID int64, newSessionID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE claude_conversations SET session_id=$1, session_started=false, updated_at=now()
		 WHERE id=$2 AND user_id=$3`,
		newSessionID, id, userID)
	return err
}

// ── Mensagens ────────────────────────────────────────────────────────────────

func (s *Server) insertMessage(ctx context.Context, m *Message) error {
	content, _ := json.Marshal(m.Content)
	var usage []byte
	if m.Usage != nil {
		usage, _ = json.Marshal(m.Usage)
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO claude_messages (conversation_id, role, kind, content, usage)
		 VALUES ($1,$2,$3,$4,$5)`,
		m.ConversationID, m.Role, m.Kind, content, usage)
	return err
}

func (s *Server) listMessages(ctx context.Context, convID string) ([]Message, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, conversation_id::text, role, kind, content, usage, created_at
		 FROM claude_messages WHERE conversation_id=$1 ORDER BY id`, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		var content []byte
		var usage []byte
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Kind, &content, &usage, &m.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(content, &m.Content)
		if len(usage) > 0 {
			_ = json.Unmarshal(usage, &m.Usage)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ── Credenciais OAuth do Claude (identidade única do agente) ──────────────────

func (s *Server) saveOAuthToken(ctx context.Context, enc []byte) error {
	_, err := s.db.Exec(ctx,
		`UPDATE claude_credentials SET oauth_token_enc=$1, status='logged_in', updated_at=now() WHERE id=1`, enc)
	return err
}

func (s *Server) clearOAuthToken(ctx context.Context) error {
	_, err := s.db.Exec(ctx,
		`UPDATE claude_credentials SET oauth_token_enc=NULL, status='logged_out', updated_at=now() WHERE id=1`)
	return err
}

func (s *Server) oauthStatus(ctx context.Context) (string, error) {
	var status string
	err := s.db.QueryRow(ctx, `SELECT status FROM claude_credentials WHERE id=1`).Scan(&status)
	return status, err
}

// oauthToken retorna o token OAuth decifrado, ou "" se deslogado.
func (s *Server) oauthToken(ctx context.Context) (string, error) {
	var enc []byte
	err := s.db.QueryRow(ctx, `SELECT oauth_token_enc FROM claude_credentials WHERE id=1`).Scan(&enc)
	if err != nil || len(enc) == 0 {
		return "", err
	}
	return decrypt(s.cfg.EncryptionKey, enc)
}
