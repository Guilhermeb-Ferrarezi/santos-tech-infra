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
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- Quando um admin liberou o client. Clients do painel nascem aprovados; os do
  -- DCR anônimo (POST /oauth/register) nascem is_active=false e sem aprovação.
  approved_at   TIMESTAMPTZ
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
  -- FK para drive_folders(id) omitida aqui (ela é criada mais abaixo neste
  -- arquivo) — a constraint real é adicionada via ALTER TABLE em db.go,
  -- depois que drive_folders já existe.
  drive_folder_id     UUID,
  drive_file_id       TEXT NOT NULL DEFAULT '',
  drive_file_name     TEXT NOT NULL DEFAULT '',
  -- Capa customizada de Reel (mesmo shape do trio acima, mesma ressalva de FK).
  drive_cover_folder_id UUID,
  drive_cover_file_id   TEXT NOT NULL DEFAULT '',
  drive_cover_file_name TEXT NOT NULL DEFAULT '',
  -- Texto alternativo de acessibilidade (só imagem estática, ver alt_text da
  -- Graph API do Instagram / alt_text_custom do Facebook).
  alt_text            TEXT NOT NULL DEFAULT '',
  -- Itens 2..10 de um carrossel — array de {folderId,fileId,fileName}; o
  -- item 1 é o trio drive_folder_id/drive_file_id/drive_file_name acima.
  carousel_items      JSONB NOT NULL DEFAULT '[]',
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

-- Configuração fixa (não por post) de localização automática do publicador
-- universal — linha única (o truque "id BOOLEAN PRIMARY KEY CHECK(id)"
-- garante isso). Ver social_publish.go / GET,PUT /social/settings.
CREATE TABLE IF NOT EXISTS social_settings (
  id                    BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
  instagram_location_id TEXT NOT NULL DEFAULT '',
  facebook_place_id     TEXT NOT NULL DEFAULT '',
  updated_by            INTEGER REFERENCES users(id) ON DELETE SET NULL,
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
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

CREATE TABLE IF NOT EXISTS downloads (
  id           BIGSERIAL PRIMARY KEY,
  name         TEXT NOT NULL,
  description  TEXT NOT NULL DEFAULT '',
  category     TEXT NOT NULL DEFAULT '',
  version      TEXT NOT NULL DEFAULT '',
  kind         TEXT NOT NULL CHECK (kind IN ('file', 'link')),
  object_key   TEXT,
  external_url TEXT,
  filename     TEXT NOT NULL DEFAULT '',
  content_type TEXT NOT NULL DEFAULT '',
  size_bytes   BIGINT,
  pinned       BOOLEAN NOT NULL DEFAULT false,
  uploaded_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  image_url    TEXT
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
  chat_adapter       TEXT NOT NULL DEFAULT 'openai_compatible',
  chat_path          TEXT NOT NULL DEFAULT '',
  chat_model         TEXT NOT NULL DEFAULT '',
  op_adapter         TEXT NOT NULL DEFAULT '',
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS push_subscriptions (
  id         BIGSERIAL PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  endpoint   TEXT NOT NULL UNIQUE,
  p256dh     TEXT NOT NULL,
  auth       TEXT NOT NULL,
  user_agent TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_push_subscriptions_user ON push_subscriptions(user_id);

CREATE TABLE IF NOT EXISTS dashboard_notifications (
  id         BIGSERIAL PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title      TEXT NOT NULL,
  body       TEXT NOT NULL DEFAULT '',
  url        TEXT NOT NULL DEFAULT '',
  read_at    TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_dashboard_notifications_user_created ON dashboard_notifications(user_id, created_at DESC);

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

-- Controle de horas de clientes (lan house/escola): cliente compra pacote de
-- horas, admin inicia/pausa/retoma/encerra sessão, link público (token) mostra
-- o cronômetro. Tempo decorrido é sempre recalculado a partir de
-- hour_session_events (nunca um contador acumulado que pode dessincronizar).
CREATE TABLE IF NOT EXISTS hour_clients (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name            TEXT NOT NULL,
  phone           TEXT,
  balance_minutes INTEGER NOT NULL DEFAULT 0,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS hour_purchases (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  client_id     UUID NOT NULL REFERENCES hour_clients(id) ON DELETE CASCADE,
  minutes_added INTEGER NOT NULL,
  note          TEXT,
  created_by    INTEGER NOT NULL REFERENCES users(id),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_hour_purchases_client ON hour_purchases(client_id);

CREATE TABLE IF NOT EXISTS hour_sessions (
  id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  client_id          UUID NOT NULL REFERENCES hour_clients(id) ON DELETE CASCADE,
  status             TEXT NOT NULL CHECK (status IN ('active', 'paused', 'ended')),
  token_hash         TEXT NOT NULL UNIQUE,
  pause_requested_at TIMESTAMPTZ,
  created_by         INTEGER NOT NULL REFERENCES users(id),
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  short_code            TEXT UNIQUE,
  short_code_expires_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_hour_sessions_client ON hour_sessions(client_id);
CREATE INDEX IF NOT EXISTS idx_hour_sessions_status ON hour_sessions(status) WHERE status != 'ended';

CREATE TABLE IF NOT EXISTS hour_session_events (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  session_id    UUID NOT NULL REFERENCES hour_sessions(id) ON DELETE CASCADE,
  event_type    TEXT NOT NULL CHECK (event_type IN ('start', 'pause', 'resume', 'end')),
  actor_user_id INTEGER NOT NULL REFERENCES users(id),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_hour_session_events_session ON hour_session_events(session_id, created_at);

-- PCs do laboratório: cada instalação do app desktop (hour-timer-app) gera um
-- device_uuid estável e manda heartbeat periódico. Nome é atribuído pelo admin
-- (nunca pelo próprio PC) para não bagunçar com quem estiver sentado nele.
-- unpair_requested e message_id/text são comandos de admin entregues no
-- próximo heartbeat: unpair_requested volta true uma única vez (o UPDATE que
-- zera roda na mesma query do upsert, ver upsertLabDeviceHeartbeat) e
-- message_id troca a cada envio para o app conseguir deduplicar no cliente.
CREATE TABLE IF NOT EXISTS hour_lab_devices (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  device_uuid         TEXT NOT NULL UNIQUE,
  name                TEXT,
  last_seen_at        TIMESTAMPTZ,
  last_ip             TEXT,
  app_version         TEXT,
  current_session_id  UUID REFERENCES hour_sessions(id) ON DELETE SET NULL,
  unpair_requested    BOOLEAN NOT NULL DEFAULT FALSE,
  message_id          UUID,
  message_text        TEXT,
  message_sent_at     TIMESTAMPTZ,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  pending_pair_token  TEXT,
  pending_pair_token_expires_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_hour_lab_devices_last_seen ON hour_lab_devices(last_seen_at);

-- Arquivos (Google Drive): o conteúdo real mora no Drive; aqui só guardamos
-- metadados de pasta e a ACL de quem enxerga/envia arquivo em cada uma — por
-- cargo (fixo ou personalizado) E por usuário individual, união dos dois.
-- O Drive só conhece uma identidade (a service account); a autorização de
-- "quem vê o quê" é toda nossa (ver drive_access.go).
CREATE TABLE IF NOT EXISTS drive_folders (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name            TEXT NOT NULL,
  description     TEXT,
  drive_folder_id TEXT NOT NULL,
  created_by      INTEGER NOT NULL REFERENCES users(id),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS drive_folder_role_access (
  folder_id  UUID NOT NULL REFERENCES drive_folders(id) ON DELETE CASCADE,
  role_kind  TEXT NOT NULL CHECK (role_kind IN ('fixed', 'custom')),
  role_value TEXT NOT NULL,
  access     TEXT NOT NULL CHECK (access IN ('read', 'write')),
  PRIMARY KEY (folder_id, role_kind, role_value)
);

CREATE TABLE IF NOT EXISTS drive_folder_members (
  folder_id UUID NOT NULL REFERENCES drive_folders(id) ON DELETE CASCADE,
  user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  access    TEXT NOT NULL CHECK (access IN ('read', 'write')),
  added_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (folder_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_drive_folder_members_user ON drive_folder_members(user_id);
