package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	agentdb "github.com/santos-tech/agent/db"
)

func newDB(ctx context.Context, url string) (*pgxpool.Pool, error) {
	// (#9) Configura o pool explicitamente em vez de usar o default (que deixa
	// MaxConns = max(4, numCPU) e sem teto de vida das conexões). Num Postgres
	// compartilhado do ecossistema, um teto baixo e a reciclagem periódica evitam
	// esgotar as conexões do servidor e acumular conexões mortas (NAT/idle timeout).
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10                      // teto de conexões deste serviço
	cfg.MaxConnLifetime = 30 * time.Minute // recicla conexões periodicamente
	cfg.MaxConnIdleTime = 5 * time.Minute  // fecha conexões ociosas

	// Compatibilidade com PgBouncer em TRANSACTION mode: o pooling por transação
	// não suporta prepared statements nomeados persistentes (eles vivem na conexão
	// do servidor, que é recompartilhada entre clientes). Com DB_PREPARED_STATEMENTS=false
	// usamos o protocolo estendido com statements anônimos (QueryExecModeExec), que
	// não persiste prepares no servidor. Sem a env (default), mantemos o comportamento
	// atual (prepared statements normais) — sem penalizar quem conecta direto ao Postgres.
	if os.Getenv("DB_PREPARED_STATEMENTS") == "false" {
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
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
  tools_disabled  BOOLEAN NOT NULL DEFAULT false,
  effort          TEXT NOT NULL DEFAULT '',
  web_search      BOOLEAN NOT NULL DEFAULT false,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_conv_user ON claude_conversations(user_id, updated_at DESC);

-- Migração idempotente: a tabela já existia em produção sem esta coluna
-- (CREATE TABLE IF NOT EXISTS é no-op e não a adicionaria).
ALTER TABLE claude_conversations ADD COLUMN IF NOT EXISTS tools_disabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE claude_conversations ADD COLUMN IF NOT EXISTS effort TEXT NOT NULL DEFAULT '';
ALTER TABLE claude_conversations ADD COLUMN IF NOT EXISTS web_search BOOLEAN NOT NULL DEFAULT false;

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

CREATE TABLE IF NOT EXISTS claude_push_tokens (
  token      TEXT PRIMARY KEY,
  user_id    BIGINT NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_push_user ON claude_push_tokens(user_id);

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

-- Um registro por invocação do CLI claude (qualquer motor). Fonte do painel de
-- gastos: total_cost_usd vem direto do envelope do CLI (--output-format json /
-- stream-json, campo "total_cost_usd" do evento "result").
CREATE TABLE IF NOT EXISTS claude_usage_events (
  id                 BIGSERIAL PRIMARY KEY,
  source             TEXT NOT NULL,               -- 'generate' | 'generate_stream' | 'session'
  task               TEXT NOT NULL DEFAULT '',     -- generateRequest.Task ("email","raw",...) quando aplicável
  model              TEXT NOT NULL DEFAULT '',
  conversation_id    UUID REFERENCES claude_conversations(id) ON DELETE SET NULL,
  total_cost_usd     DOUBLE PRECISION NOT NULL DEFAULT 0,
  input_tokens       BIGINT NOT NULL DEFAULT 0,
  output_tokens      BIGINT NOT NULL DEFAULT 0,
  cache_read_tokens  BIGINT NOT NULL DEFAULT 0,
  cache_write_tokens BIGINT NOT NULL DEFAULT 0,
  duration_ms        BIGINT NOT NULL DEFAULT 0,
  is_error           BOOLEAN NOT NULL DEFAULT false,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_usage_created ON claude_usage_events(created_at DESC);
`

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, migration)
	return err
}

// ── helpers de conversão pgtype ↔ tipos do serviço ──────────────────────────

// uuidFromStr converte uma string UUID para pgtype.UUID.
func uuidFromStr(s string) pgtype.UUID {
	var u pgtype.UUID
	_ = u.Scan(s)
	return u
}

// convFromGetRow converte uma GetConversationRow gerada pelo sqlc para *Conversation.
func convFromGetRow(r agentdb.GetConversationRow) *Conversation {
	return &Conversation{
		ID:             r.ID,
		UserID:         r.UserID,
		Title:          r.Title,
		Repo:           r.Repo,
		Workdir:        r.Workdir,
		Model:          r.Model,
		Status:         r.Status,
		SessionID:      r.SessionID,
		SessionStarted: r.SessionStarted,
		ToolsDisabled:  r.ToolsDisabled,
		Effort:         r.Effort,
		WebSearch:      r.WebSearch,
		CreatedAt:      r.CreatedAt.Time,
		UpdatedAt:      r.UpdatedAt.Time,
	}
}

// convFromListRow converte uma ListConversationsRow para Conversation.
func convFromListRow(r agentdb.ListConversationsRow) Conversation {
	return Conversation{
		ID:             r.ID,
		UserID:         r.UserID,
		Title:          r.Title,
		Repo:           r.Repo,
		Workdir:        r.Workdir,
		Model:          r.Model,
		Status:         r.Status,
		SessionID:      r.SessionID,
		SessionStarted: r.SessionStarted,
		ToolsDisabled:  r.ToolsDisabled,
		Effort:         r.Effort,
		WebSearch:      r.WebSearch,
		CreatedAt:      r.CreatedAt.Time,
		UpdatedAt:      r.UpdatedAt.Time,
	}
}

// ── Papel do usuário (para o admin guard) ────────────────────────────────────

func (s *Server) userRole(ctx context.Context, userID int64) (int16, error) {
	role, err := s.q.UserRole(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return role, err
}

// userIDByAPIKeyHash valida um Personal Access Token (tabela api_keys compartilhada
// com o auth) pelo hash: devolve o user_id se válido e não expirado, marcando o
// último uso. (0, nil) significa token inválido/expirado.
func (s *Server) userIDByAPIKeyHash(ctx context.Context, hash string) (int64, error) {
	uid, err := s.q.UserIDByAPIKeyHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return int64(uid), err
}

// ── Conversas ────────────────────────────────────────────────────────────────

func (s *Server) insertConversation(ctx context.Context, c *Conversation) error {
	return s.q.InsertConversation(ctx, agentdb.InsertConversationParams{
		ID:             uuidFromStr(c.ID),
		UserID:         c.UserID,
		Title:          c.Title,
		Repo:           c.Repo,
		Workdir:        c.Workdir,
		Model:          c.Model,
		Status:         c.Status,
		SessionID:      uuidFromStr(c.SessionID),
		SessionStarted: c.SessionStarted,
		ToolsDisabled:  c.ToolsDisabled,
		Effort:         c.Effort,
		WebSearch:      c.WebSearch,
	})
}

func (s *Server) conversationByID(ctx context.Context, id string, userID int64) (*Conversation, error) {
	row, err := s.q.GetConversation(ctx, agentdb.GetConversationParams{
		Column1: uuidFromStr(id),
		UserID:  userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return convFromGetRow(row), nil
}

func (s *Server) listConversations(ctx context.Context, userID int64) ([]Conversation, error) {
	rows, err := s.q.ListConversations(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Conversation, len(rows))
	for i, r := range rows {
		out[i] = convFromListRow(r)
	}
	return out, nil
}

func (s *Server) deleteConversation(ctx context.Context, id string, userID int64) (bool, error) {
	n, err := s.q.DeleteConversation(ctx, agentdb.DeleteConversationParams{
		Column1: uuidFromStr(id),
		UserID:  userID,
	})
	return n > 0, err
}

func (s *Server) updateConversationModel(ctx context.Context, id string, userID int64, model string) error {
	return s.q.UpdateConversationModel(ctx, agentdb.UpdateConversationModelParams{
		Model:   model,
		Column2: uuidFromStr(id),
		UserID:  userID,
	})
}

func (s *Server) updateConversationTitle(ctx context.Context, id string, userID int64, title string) error {
	return s.q.UpdateConversationTitle(ctx, agentdb.UpdateConversationTitleParams{
		Title:   &title,
		Column2: uuidFromStr(id),
		UserID:  userID,
	})
}

// setTitleIfEmpty define o título só se ainda estiver vazio (usado pelo auto-título).
func (s *Server) setTitleIfEmpty(ctx context.Context, id, title string) error {
	if s.q == nil {
		slog.Warn("setTitleIfEmpty: queries nil (esperado só em teste; em produção indica misconfiguração)")
		return nil
	}
	return s.q.SetTitleIfEmpty(ctx, agentdb.SetTitleIfEmptyParams{
		Title:   &title,
		Column2: uuidFromStr(id),
	})
}

func (s *Server) setConversationStatus(ctx context.Context, id, status string) error {
	if s.q == nil {
		slog.Warn("setConversationStatus: queries nil (esperado só em teste; em produção indica misconfiguração)")
		return nil
	}
	return s.q.SetConversationStatus(ctx, agentdb.SetConversationStatusParams{
		Status:  status,
		Column2: uuidFromStr(id),
	})
}

// markSessionStarted marca que o 1º turno da sessão atual já rodou (próximos usam --resume).
func (s *Server) markSessionStarted(ctx context.Context, id string) error {
	if s.q == nil {
		slog.Warn("markSessionStarted: queries nil (esperado só em teste; em produção indica misconfiguração)")
		return nil
	}
	return s.q.MarkSessionStarted(ctx, uuidFromStr(id))
}

// rotateSession gera um novo claude session_id e zera o "started" — contexto novo.
// Preserva o id (PK) e as mensagens. Usado por /clear e /compact.
func (s *Server) rotateSession(ctx context.Context, id string, userID int64, newSessionID string) error {
	return s.q.RotateSession(ctx, agentdb.RotateSessionParams{
		Column1: uuidFromStr(newSessionID),
		Column2: uuidFromStr(id),
		UserID:  userID,
	})
}

// ── Mensagens ────────────────────────────────────────────────────────────────

func (s *Server) insertMessage(ctx context.Context, m *Message) error {
	if s.q == nil {
		slog.Warn("insertMessage: queries nil (esperado só em teste; em produção indica misconfiguração)")
		return nil
	}
	content, _ := json.Marshal(m.Content)
	var usage []byte
	if m.Usage != nil {
		usage, _ = json.Marshal(m.Usage)
	}
	return s.q.InsertMessage(ctx, agentdb.InsertMessageParams{
		Column1: uuidFromStr(m.ConversationID),
		Role:    m.Role,
		Kind:    m.Kind,
		Content: content,
		Usage:   usage,
	})
}

func (s *Server) listMessages(ctx context.Context, convID string) ([]Message, error) {
	rows, err := s.q.ListMessages(ctx, uuidFromStr(convID))
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(rows))
	for _, r := range rows {
		var m Message
		m.ID = r.ID
		m.ConversationID = r.ConversationID
		m.Role = r.Role
		m.Kind = r.Kind
		m.CreatedAt = r.CreatedAt.Time
		_ = json.Unmarshal(r.Content, &m.Content)
		if len(r.Usage) > 0 {
			_ = json.Unmarshal(r.Usage, &m.Usage)
		}
		out = append(out, m)
	}
	return out, nil
}

// ── Credenciais OAuth do Claude (identidade única do agente) ──────────────────

func (s *Server) saveOAuthToken(ctx context.Context, enc []byte) error {
	return s.q.SaveOAuthToken(ctx, enc)
}

func (s *Server) clearOAuthToken(ctx context.Context) error {
	return s.q.ClearOAuthToken(ctx)
}

func (s *Server) oauthStatus(ctx context.Context) (string, error) {
	return s.q.GetOAuthStatus(ctx)
}

// oauthToken retorna o token OAuth decifrado, ou "" se deslogado. Sem banco (q nil),
// retorna "" — o claudeEnv então não injeta CLAUDE_CODE_OAUTH_TOKEN e o CLI usa as
// credenciais locais do volume (~/.claude/.credentials.json), como no fluxo logged_out.
func (s *Server) oauthToken(ctx context.Context) (string, error) {
	if s.q == nil {
		return "", nil
	}
	enc, err := s.q.GetOAuthToken(ctx)
	if err != nil || len(enc) == 0 {
		return "", err
	}
	return decrypt(s.cfg.EncryptionKey, enc)
}

// ── Push tokens (Expo) ───────────────────────────────────────────────────────

func (s *Server) savePushToken(ctx context.Context, userID int64, token string) error {
	return s.q.SavePushToken(ctx, agentdb.SavePushTokenParams{Token: token, UserID: userID})
}

func (s *Server) pushTokensForUser(ctx context.Context, userID int64) ([]string, error) {
	return s.q.PushTokensForUser(ctx, userID)
}
