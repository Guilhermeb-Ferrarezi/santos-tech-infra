-- Schema consolidado do agent-go.
-- Extraído da migração embutida em db.go (const migration).
-- A tabela `users` vive no Postgres compartilhado (gerida pelo auth) e é referenciada aqui.

CREATE TABLE IF NOT EXISTS claude_conversations (
  id              UUID PRIMARY KEY,
  user_id         BIGINT NOT NULL,
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

CREATE TABLE IF NOT EXISTS claude_push_tokens (
  token      TEXT PRIMARY KEY,
  user_id    BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_push_user ON claude_push_tokens(user_id);

CREATE TABLE IF NOT EXISTS api_keys (
  id           BIGSERIAL PRIMARY KEY,
  user_id      INTEGER NOT NULL,
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

-- Tabela compartilhada do ecossistema (gerida pelo api-go/auth).
-- Incluída aqui apenas para que o sqlc resolva as referências de query.
CREATE TABLE IF NOT EXISTS users (
  id   BIGSERIAL PRIMARY KEY,
  role SMALLINT NOT NULL DEFAULT 1
);
