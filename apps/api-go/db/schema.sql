-- Schema reconstruído a partir das queries do api-go.
-- As tabelas base (users, sessions, oauth_accounts, custom_roles) são criadas pelo
-- Drizzle (outro repo). Este schema é usado apenas pelo sqlc para geração de código.

CREATE TABLE IF NOT EXISTS users (
  id               SERIAL PRIMARY KEY,
  email            TEXT NOT NULL UNIQUE,
  username         TEXT,
  name             TEXT NOT NULL DEFAULT '',
  password_hash    TEXT,
  avatar_url       TEXT,
  role             SMALLINT NOT NULL DEFAULT 1,
  custom_role_id   UUID,
  suspended_at     TIMESTAMPTZ,
  mfa_enabled      BOOLEAN NOT NULL DEFAULT false,
  totp_secret      TEXT,
  preferences      JSONB NOT NULL DEFAULT '{}',
  quota_bytes      BIGINT NOT NULL DEFAULT 524288000,
  email_verified_at TIMESTAMPTZ,
  mfa_method       TEXT NOT NULL DEFAULT 'totp',
  login_disabled   BOOLEAN NOT NULL DEFAULT false,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sessions (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  refresh_token_hash  TEXT NOT NULL,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at          TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS oauth_accounts (
  id          SERIAL PRIMARY KEY,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider    TEXT NOT NULL,
  provider_id TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (provider, provider_id)
);

CREATE TABLE IF NOT EXISTS custom_roles (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name        TEXT NOT NULL DEFAULT '',
  description TEXT,
  permissions JSONB NOT NULL DEFAULT '{}',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS recovery_codes (
  id         BIGSERIAL PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash  TEXT NOT NULL,
  used_at    TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

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

CREATE TABLE IF NOT EXISTS oauth_clients (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  client_id     TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL,
  redirect_uris TEXT[] NOT NULL,
  is_active     BOOLEAN NOT NULL DEFAULT true,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS boards (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title         TEXT NOT NULL,
  scene         JSONB NOT NULL DEFAULT '{}',
  scene_version INTEGER NOT NULL DEFAULT 0,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS board_members (
  board_id  UUID NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role      TEXT NOT NULL CHECK (role IN ('viewer','editor')),
  added_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (board_id, user_id)
);

CREATE TABLE IF NOT EXISTS social_posts (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  title               TEXT NOT NULL,
  caption             TEXT NOT NULL DEFAULT '',
  platform            TEXT NOT NULL,
  pilar               TEXT NOT NULL,
  status              TEXT NOT NULL DEFAULT 'ideia',
  scheduled_at        TIMESTAMPTZ,
  media_url           TEXT NOT NULL DEFAULT '',
  reference_url       TEXT NOT NULL DEFAULT '',
  formato             TEXT NOT NULL DEFAULT 'estatico',
  objetivo            TEXT NOT NULL DEFAULT 'alcance',
  programa            TEXT NOT NULL DEFAULT '',
  receita             TEXT NOT NULL DEFAULT '',
  plataformas_destino TEXT[] NOT NULL DEFAULT '{}',
  copy_arte           JSONB NOT NULL DEFAULT '[]',
  hashtags            TEXT[] NOT NULL DEFAULT '{}',
  conceito_visual     TEXT NOT NULL DEFAULT '',
  paleta              JSONB NOT NULL DEFAULT '{}',
  prompt_ia           TEXT NOT NULL DEFAULT '',
  specs               JSONB NOT NULL DEFAULT '{}',
  master_url          TEXT NOT NULL DEFAULT '',
  mandatorios         TEXT NOT NULL DEFAULT '',
  responsavel_id      INTEGER REFERENCES users(id) ON DELETE SET NULL,
  funil_etapa         TEXT NOT NULL DEFAULT '',
  created_by          INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS social_post_notes (
  id         BIGSERIAL PRIMARY KEY,
  post_id    UUID NOT NULL REFERENCES social_posts(id) ON DELETE CASCADE,
  author_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  content    TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS social_post_status_history (
  id         BIGSERIAL PRIMARY KEY,
  post_id    UUID NOT NULL REFERENCES social_posts(id) ON DELETE CASCADE,
  changed_by BIGINT REFERENCES users(id),
  old_status TEXT NOT NULL,
  new_status TEXT NOT NULL,
  changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- mfa_email_codes table (kept as comment — handled via Redis, not DB)
-- No separate table needed.

CREATE TABLE IF NOT EXISTS ip_bans (
  id         BIGSERIAL PRIMARY KEY,
  ip         TEXT NOT NULL UNIQUE,
  reason     TEXT,
  banned_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  expires_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS model3d_file (
  id           BIGSERIAL PRIMARY KEY,
  filename     TEXT NOT NULL,
  object_key   TEXT NOT NULL,
  ext          TEXT NOT NULL,
  content_type TEXT NOT NULL,
  size_bytes   BIGINT NOT NULL,
  uploaded_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  folder       TEXT NOT NULL DEFAULT '',
  pinned       BOOLEAN NOT NULL DEFAULT false,
  thumbnail_key TEXT
);

CREATE TABLE IF NOT EXISTS api_router_providers (
  id                 BIGSERIAL PRIMARY KEY,
  name               TEXT NOT NULL UNIQUE,
  base_url           TEXT NOT NULL,
  auth_header        TEXT NOT NULL DEFAULT 'Authorization',
  auth_scheme        TEXT NOT NULL DEFAULT 'Bearer',
  unauthorized_codes INTEGER[] NOT NULL DEFAULT ARRAY[401],
  no_credit_codes    INTEGER[] NOT NULL DEFAULT ARRAY[402, 429],
  test_path          TEXT NOT NULL DEFAULT '',
  test_method        TEXT NOT NULL DEFAULT 'GET',
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS api_router_keys (
  id              BIGSERIAL PRIMARY KEY,
  provider_id     BIGINT NOT NULL REFERENCES api_router_providers(id) ON DELETE CASCADE,
  label           TEXT NOT NULL,
  secret_enc      TEXT NOT NULL,
  secret_tail     TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT 'active',
  priority        INTEGER NOT NULL DEFAULT 0,
  failure_count   INTEGER NOT NULL DEFAULT 0,
  last_used_at    TIMESTAMPTZ,
  last_error_at   TIMESTAMPTZ,
  last_error_code INTEGER,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
