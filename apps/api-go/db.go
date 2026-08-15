package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newDB(ctx context.Context, url string) (*pgxpool.Pool, error) {
	// Configura o pool explicitamente (em vez de pgxpool.New) para limitar conexões
	// e reciclar as antigas. Sem isso o default é ilimitado/quase-eterno, o que sob
	// carga abre conexões demais no Postgres e mantém sockets velhos vivos.
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	// Compatibilidade com PgBouncer em transaction mode: ele não suporta prepared
	// statements nomeados persistentes (cada query pode cair numa conexão diferente).
	// Com DB_PREPARED_STATEMENTS=false usamos o protocolo estendido com statements
	// anônimos (QueryExecModeExec), seguro sob transaction pooling. Sem a env, mantemos
	// o comportamento padrão do pgx (prepared statements), ideal para conexão direta.
	if os.Getenv("DB_PREPARED_STATEMENTS") == "false" {
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	}
	cfg.MaxConns = 10                      // teto de conexões simultâneas do pool
	cfg.MaxConnLifetime = 30 * time.Minute // recicla conexões periodicamente
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

// migrate adiciona apenas o que é novo (MFA, preferências). As tabelas base já existem (Drizzle).
const migration = `
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS preferences JSONB NOT NULL DEFAULT '{}';
ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_bytes BIGINT NOT NULL DEFAULT 524288000;
ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verified_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_method TEXT NOT NULL DEFAULT 'totp';
ALTER TABLE users ADD COLUMN IF NOT EXISTS login_disabled BOOLEAN NOT NULL DEFAULT false;
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
CREATE INDEX IF NOT EXISTS idx_boards_owner ON boards(owner_id);
CREATE TABLE IF NOT EXISTS board_members (
  board_id  UUID NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role      TEXT NOT NULL CHECK (role IN ('viewer','editor')),
  added_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (board_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_board_members_user ON board_members(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);
ALTER TABLE custom_roles ADD COLUMN IF NOT EXISTS name        TEXT NOT NULL DEFAULT '';
ALTER TABLE custom_roles ADD COLUMN IF NOT EXISTS description TEXT;
ALTER TABLE custom_roles ADD COLUMN IF NOT EXISTS permissions JSONB NOT NULL DEFAULT '{}';
ALTER TABLE custom_roles ADD COLUMN IF NOT EXISTS created_at  TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE custom_roles ADD COLUMN IF NOT EXISTS updated_at  TIMESTAMPTZ NOT NULL DEFAULT now();
-- Client OAuth fixo do app mobile (org/mobile-dash). client_id estável e
-- conhecido pelo app; PKCE público (sem secret). Idempotente.
INSERT INTO oauth_clients (client_id, name, redirect_uris)
VALUES ('santos-tech-mobile', 'Santos Tech Mobile', ARRAY['santostech://auth'])
ON CONFLICT (client_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS social_posts (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  title         TEXT NOT NULL,
  caption       TEXT NOT NULL DEFAULT '',
  platform      TEXT NOT NULL CHECK (platform IN ('facebook','instagram','tiktok','twitter_x','threads','youtube','linkedin')),
  pilar         TEXT NOT NULL CHECK (pilar IN ('educacional','institucional','bastidores','produto','engajamento')),
  status        TEXT NOT NULL DEFAULT 'ideia' CHECK (status IN ('ideia','planejado','em_producao','revisao','agendado','publicado')),
  scheduled_at  TIMESTAMPTZ,
  media_url     TEXT NOT NULL DEFAULT '',
  reference_url TEXT NOT NULL DEFAULT '',
  created_by    INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_social_posts_status ON social_posts(status);
CREATE INDEX IF NOT EXISTS idx_social_posts_scheduled_at ON social_posts(scheduled_at) WHERE scheduled_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS social_post_notes (
  id         BIGSERIAL PRIMARY KEY,
  post_id    UUID NOT NULL REFERENCES social_posts(id) ON DELETE CASCADE,
  author_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  content    TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_social_post_notes_post ON social_post_notes(post_id);
CREATE TABLE IF NOT EXISTS social_post_status_history (
  id          BIGSERIAL PRIMARY KEY,
  post_id     UUID NOT NULL REFERENCES social_posts(id) ON DELETE CASCADE,
  changed_by  BIGINT REFERENCES users(id),
  old_status  TEXT NOT NULL,
  new_status  TEXT NOT NULL,
  changed_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_social_post_status_history_post ON social_post_status_history(post_id);
-- Migração: normalizar valores antigos de plataforma/pilar para os corretos
UPDATE social_posts SET pilar='institucional' WHERE pilar='produto';
UPDATE social_posts SET pilar='educacional'   WHERE pilar='engajamento';
-- Recriar constraints com os valores do handoff (drop+add é idempotente)
ALTER TABLE social_posts DROP CONSTRAINT IF EXISTS social_posts_platform_check;
ALTER TABLE social_posts ADD CONSTRAINT social_posts_platform_check
  CHECK (platform IN ('facebook','instagram','tiktok','youtube','twitter_x','threads','google_meu_negocio','blog','linkedin'));
ALTER TABLE social_posts DROP CONSTRAINT IF EXISTS social_posts_pilar_check;
ALTER TABLE social_posts ADD CONSTRAINT social_posts_pilar_check
  CHECK (pilar IN ('educacional','institucional','captacao','prova_social','bastidores','tech_mundo_real'));
ALTER TABLE social_posts DROP CONSTRAINT IF EXISTS social_posts_status_check;
ALTER TABLE social_posts ADD CONSTRAINT social_posts_status_check
  CHECK (status IN ('ideia','planejado','em_producao','revisao','aprovado','agendado','publicado','arquivado'));
ALTER TABLE social_posts
  ADD COLUMN IF NOT EXISTS formato             text   NOT NULL DEFAULT 'estatico',
  ADD COLUMN IF NOT EXISTS objetivo            text   NOT NULL DEFAULT 'alcance',
  ADD COLUMN IF NOT EXISTS programa            text   NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS receita             text   NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS plataformas_destino text[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS copy_arte           jsonb  NOT NULL DEFAULT '[]',
  ADD COLUMN IF NOT EXISTS hashtags            text[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS conceito_visual     text   NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS paleta              jsonb  NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS prompt_ia           text   NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS specs               jsonb  NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS master_url          text   NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS mandatorios         text   NOT NULL DEFAULT '';
ALTER TABLE social_posts DROP CONSTRAINT IF EXISTS social_posts_formato_check;
ALTER TABLE social_posts ADD CONSTRAINT social_posts_formato_check
  CHECK (formato IN ('estatico','carrossel','reel','story','video_longo','short','thumbnail','card_link'));
ALTER TABLE social_posts DROP CONSTRAINT IF EXISTS social_posts_objetivo_check;
ALTER TABLE social_posts ADD CONSTRAINT social_posts_objetivo_check
  CHECK (objetivo IN ('alcance','engajamento','conversao','autoridade'));
ALTER TABLE social_posts DROP CONSTRAINT IF EXISTS social_posts_programa_check;
ALTER TABLE social_posts ADD CONSTRAINT social_posts_programa_check
  CHECK (programa IN ('','create','jr','camps','academies'));
ALTER TABLE social_posts DROP CONSTRAINT IF EXISTS social_posts_receita_check;
ALTER TABLE social_posts ADD CONSTRAINT social_posts_receita_check
  CHECK (receita IN ('','capa_gancho','hero_numero','versus','antes_depois','desenvolvimento','cta_fechamento','checklist','passo_a_passo','citacao_depoimento','poster_anuncio'));
CREATE TABLE IF NOT EXISTS ip_bans (
  id         BIGSERIAL PRIMARY KEY,
  ip         TEXT NOT NULL UNIQUE,
  reason     TEXT,
  banned_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  expires_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ip_bans_ip ON ip_bans(ip);

CREATE TABLE IF NOT EXISTS blog_categories (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  slug       TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS blog_posts (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  slug            TEXT NOT NULL UNIQUE,
  title           TEXT NOT NULL,
  excerpt         TEXT NOT NULL DEFAULT '',
  content_html    TEXT NOT NULL DEFAULT '',
  cover_image_url TEXT,
  category_id     UUID NOT NULL REFERENCES blog_categories(id),
  author_id       INTEGER REFERENCES users(id) ON DELETE SET NULL,
  status          TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published')),
  published_at    TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_blog_posts_status ON blog_posts(status);
CREATE INDEX IF NOT EXISTS idx_blog_posts_category ON blog_posts(category_id);
ALTER TABLE social_posts ADD COLUMN IF NOT EXISTS responsavel_id INTEGER REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_social_posts_responsavel ON social_posts(responsavel_id);
ALTER TABLE social_posts ADD COLUMN IF NOT EXISTS funil_etapa TEXT NOT NULL DEFAULT '';
ALTER TABLE social_posts DROP CONSTRAINT IF EXISTS social_posts_funil_etapa_check;
ALTER TABLE social_posts ADD CONSTRAINT social_posts_funil_etapa_check
  CHECK (funil_etapa IN ('','topo','meio','fundo'));
CREATE TABLE IF NOT EXISTS task_categories (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS tasks (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  title          TEXT NOT NULL,
  description    TEXT NOT NULL DEFAULT '',
  category_id    UUID REFERENCES task_categories(id) ON DELETE SET NULL,
  status         TEXT NOT NULL DEFAULT 'a_fazer' CHECK (status IN ('a_fazer','em_andamento','concluida','cancelada')),
  priority       TEXT NOT NULL DEFAULT 'media' CHECK (priority IN ('baixa','media','alta')),
  due_date       TIMESTAMPTZ,
  responsavel_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_responsavel ON tasks(responsavel_id);
CREATE INDEX IF NOT EXISTS idx_tasks_created_by ON tasks(created_by);
CREATE INDEX IF NOT EXISTS idx_tasks_category ON tasks(category_id);
CREATE TABLE IF NOT EXISTS task_notes (
  id         BIGSERIAL PRIMARY KEY,
  task_id    UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  author_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  content    TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_task_notes_task ON task_notes(task_id);
-- Seed inicial de categorias — idempotente, admin edita/apaga depois pela UI.
INSERT INTO task_categories (name) SELECT 'Financeiro' WHERE NOT EXISTS (SELECT 1 FROM task_categories);
INSERT INTO task_categories (name) SELECT 'Compras' WHERE NOT EXISTS (SELECT 1 FROM task_categories WHERE name='Compras');
INSERT INTO task_categories (name) SELECT 'Cliente' WHERE NOT EXISTS (SELECT 1 FROM task_categories WHERE name='Cliente');
INSERT INTO task_categories (name) SELECT 'Recepção' WHERE NOT EXISTS (SELECT 1 FROM task_categories WHERE name='Recepção');
INSERT INTO task_categories (name) SELECT 'Pedagógico' WHERE NOT EXISTS (SELECT 1 FROM task_categories WHERE name='Pedagógico');
INSERT INTO task_categories (name) SELECT 'Manutenção' WHERE NOT EXISTS (SELECT 1 FROM task_categories WHERE name='Manutenção');
INSERT INTO task_categories (name) SELECT 'Outro' WHERE NOT EXISTS (SELECT 1 FROM task_categories WHERE name='Outro');
CREATE TABLE IF NOT EXISTS glossary_terms (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  term       TEXT NOT NULL,
  definicao  TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_glossary_terms_term_lower ON glossary_terms(lower(term));
-- Seed inicial — idempotente, admin edita/apaga depois pela UI.
INSERT INTO glossary_terms (term, definicao) SELECT 'A-roll', 'A parte principal do vídeo — geralmente a pessoa falando direto pra câmera.' WHERE NOT EXISTS (SELECT 1 FROM glossary_terms WHERE lower(term)='a-roll');
INSERT INTO glossary_terms (term, definicao) SELECT 'B-roll', 'As imagens extras que aparecem por cima da fala, tipo cenas de apoio, pra deixar o vídeo mais interessante.' WHERE NOT EXISTS (SELECT 1 FROM glossary_terms WHERE lower(term)='b-roll');
INSERT INTO glossary_terms (term, definicao) SELECT 'Crop', 'Cortar as bordas de uma imagem ou vídeo, tipo recortar uma foto.' WHERE NOT EXISTS (SELECT 1 FROM glossary_terms WHERE lower(term)='crop');
INSERT INTO glossary_terms (term, definicao) SELECT 'Take', 'Cada tentativa de gravação de uma mesma cena. "Terceiro take" = a terceira vez que gravou aquela cena.' WHERE NOT EXISTS (SELECT 1 FROM glossary_terms WHERE lower(term)='take');
INSERT INTO glossary_terms (term, definicao) SELECT 'Blur', 'Deixar uma parte da imagem embaçada de propósito, pra esconder algo ou destacar outra coisa.' WHERE NOT EXISTS (SELECT 1 FROM glossary_terms WHERE lower(term)='blur');
INSERT INTO glossary_terms (term, definicao) SELECT 'Build', 'O processo de transformar o código em algo que roda de verdade no site, tipo montar as peças de um quebra-cabeça.' WHERE NOT EXISTS (SELECT 1 FROM glossary_terms WHERE lower(term)='build');
INSERT INTO glossary_terms (term, definicao) SELECT 'Lint', 'Uma checagem automática que avisa se o código tem algum erro de estilo ou descuido, antes de ir pro ar.' WHERE NOT EXISTS (SELECT 1 FROM glossary_terms WHERE lower(term)='lint');
CREATE TABLE IF NOT EXISTS blog_events (
  id          BIGSERIAL PRIMARY KEY,
  type        TEXT NOT NULL CHECK (type IN ('pageview','cta_click')),
  post_slug   TEXT,
  path        TEXT NOT NULL,
  session_id  TEXT NOT NULL,
  visitor_id  TEXT NOT NULL,
  referrer    TEXT,
  utm_source  TEXT,
  device      TEXT NOT NULL DEFAULT '',
  browser     TEXT,
  os          TEXT,
  country     TEXT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_blog_events_post_created ON blog_events(post_slug, created_at);
CREATE INDEX IF NOT EXISTS idx_blog_events_type_created ON blog_events(type, created_at);
CREATE INDEX IF NOT EXISTS idx_blog_events_created ON blog_events(created_at);
-- Transição y_px (pixel absoluto) -> y_pct (0..1, mesma normalização do
-- x_pct) — feature lançada há poucas horas, sem dado real acumulado ainda,
-- então dropar é seguro. Idempotente: só roda se a coluna antiga existir; em
-- boots seguintes (schema já migrado) é um no-op.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='blog_heatmap_clicks' AND column_name='y_px') THEN
    DROP TABLE blog_heatmap_clicks;
  END IF;
END $$;
CREATE TABLE IF NOT EXISTS blog_heatmap_clicks (
  id          BIGSERIAL PRIMARY KEY,
  post_slug   TEXT NOT NULL,
  viewport    TEXT NOT NULL CHECK (viewport IN ('mobile','desktop')),
  x_pct       REAL NOT NULL CHECK (x_pct >= 0 AND x_pct <= 1),
  y_pct       REAL NOT NULL CHECK (y_pct >= 0 AND y_pct <= 1),
  session_id  TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_blog_heatmap_clicks_post_vp_created ON blog_heatmap_clicks(post_slug, viewport, created_at);
CREATE TABLE IF NOT EXISTS blog_heatmap_scroll (
  id             BIGSERIAL PRIMARY KEY,
  post_slug      TEXT NOT NULL,
  viewport       TEXT NOT NULL CHECK (viewport IN ('mobile','desktop')),
  max_depth_pct  REAL NOT NULL CHECK (max_depth_pct >= 0 AND max_depth_pct <= 1),
  session_id     TEXT NOT NULL,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_blog_heatmap_scroll_post_vp_created ON blog_heatmap_scroll(post_slug, viewport, created_at);

CREATE TABLE IF NOT EXISTS model3d_file (
  id           BIGSERIAL PRIMARY KEY,
  filename     TEXT NOT NULL,
  object_key   TEXT NOT NULL,
  ext          TEXT NOT NULL,
  content_type TEXT NOT NULL,
  size_bytes   BIGINT NOT NULL,
  uploaded_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_model3d_file_created ON model3d_file(created_at DESC);

ALTER TABLE model3d_file ADD COLUMN IF NOT EXISTS folder TEXT NOT NULL DEFAULT '';
ALTER TABLE model3d_file ADD COLUMN IF NOT EXISTS pinned BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS idx_model3d_file_folder ON model3d_file(folder);
ALTER TABLE model3d_file ADD COLUMN IF NOT EXISTS thumbnail_key TEXT;

-- Mapeamento comentário do Instagram -> link de destino (automação de private
-- reply, substitui o ManyChat). media_id é o id numérico da publicação no IG;
-- cada publicação tem no máximo um link mapeado.
CREATE TABLE IF NOT EXISTS instagram_comment_links (
  media_id   TEXT PRIMARY KEY,
  url        TEXT NOT NULL,
  note       TEXT NOT NULL DEFAULT '',
  created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Palavra-chave opcional: só responde se o texto do comentário contiver ela
-- (case-insensitive). Vazio = responde a qualquer comentário na publicação.
ALTER TABLE instagram_comment_links ADD COLUMN IF NOT EXISTS keyword TEXT NOT NULL DEFAULT '';

-- Roteador de chaves de API: um "provider" agrupa N chaves rotacionadas em
-- ordem (priority, id) até uma responder fora dos códigos de
-- unauthorized_codes (401 por padrão) / no_credit_codes (402/429 por padrão).
-- Nome prefixado "api_router_" para não colidir com api_keys (PATs de usuário).
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
CREATE INDEX IF NOT EXISTS idx_api_router_keys_provider ON api_router_keys(provider_id);
-- Família de adapter usada por /chat pra montar a requisição no formato nativo
-- do provider e interpretar a resposta (ver apirouter_adapters.go).
ALTER TABLE api_router_providers ADD COLUMN IF NOT EXISTS chat_adapter TEXT NOT NULL DEFAULT 'openai_compatible';
ALTER TABLE api_router_providers ADD COLUMN IF NOT EXISTS chat_path    TEXT NOT NULL DEFAULT '';
ALTER TABLE api_router_providers ADD COLUMN IF NOT EXISTS chat_model   TEXT NOT NULL DEFAULT '';
-- Família de adapter das operações normalizadas (/op/{transcribe|tts|image|predict}),
-- ver apirouter_ops.go. Vazio = provider só suporta chat/proxy.
ALTER TABLE api_router_providers ADD COLUMN IF NOT EXISTS op_adapter   TEXT NOT NULL DEFAULT '';
-- Confirmação de publicação por plataforma (uma linha = "essa rede recebeu essa peça").
-- UNIQUE garante upsert (reconfirmar atualiza, não duplica). confirmed_by vem sempre do
-- usuário autenticado da sessão no handler, nunca de um valor mandado pelo cliente.
CREATE TABLE IF NOT EXISTS social_post_platform_confirmations (
  id           BIGSERIAL PRIMARY KEY,
  post_id      UUID NOT NULL REFERENCES social_posts(id) ON DELETE CASCADE,
  platform     TEXT NOT NULL,
  confirmed_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
  confirmed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (post_id, platform)
);
CREATE INDEX IF NOT EXISTS idx_social_post_platform_confirmations_post ON social_post_platform_confirmations(post_id);

-- Web Push (notificações do navegador — nova tarefa, novo email). endpoint é
-- único porque o mesmo dispositivo/navegador reaparece com o mesmo endpoint
-- ao re-subscrever (upsert em vez de duplicar).
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

-- Histórico de notificações (sino no header do dashboard) — registrado junto
-- com todo disparo de Web Push, pra existir uma central com histórico mesmo
-- pra quem não ativou push ou estava com a aba fechada quando chegou.
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
`

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, migration)
	return err
}

// uuid colunas vêm com ::text pra escanear direto em string.
const userCols = `id, email, username, name, password_hash, avatar_url, role, custom_role_id::text, mfa_enabled, totp_secret, suspended_at, created_at, preferences, quota_bytes, email_verified_at, mfa_method, login_disabled`

// userCols com prefixo "u." pra queries com JOIN em sessions.
var userCols2 = "u." + strings.ReplaceAll(userCols, ", ", ", u.")

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Username, &u.Name, &u.PasswordHash, &u.AvatarURL,
		&u.Role, &u.CustomRoleID, &u.MFAEnabled, &u.TOTPSecret, &u.SuspendedAt, &u.CreatedAt, &u.Preferences, &u.QuotaBytes, &u.EmailVerifiedAt, &u.MFAMethod, &u.LoginDisabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	u.HasTOTPSecret = u.TOTPSecret != nil
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

// insertUserWithRoleAndPassword cria um usuário já ATIVO (com senha) e o role dado.
// Diferente de insertUserWithRole, não requer convite: o usuário pode logar
// imediatamente com o email e a senha fornecidos.
func (s *Server) insertUserWithRoleAndPassword(ctx context.Context, email, name, passwordHash string, role int16) (*User, error) {
	return scanUser(s.db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, role) VALUES ($1,$2,$3,$4) RETURNING `+userCols,
		email, name, passwordHash, role))
}

// insertSharedMailbox cria uma caixa institucional @santos-tech.com SEM senha e com
// login_disabled=true: recebe/envia email, mas não autentica por nenhum caminho.
func (s *Server) insertSharedMailbox(ctx context.Context, email, name string) (*User, error) {
	return scanUser(s.db.QueryRow(ctx,
		`INSERT INTO users (email, name, role, login_disabled) VALUES ($1,$2,$3,true) RETURNING `+userCols,
		email, name, RoleStudent))
}

// collectUsers escaneia todas as linhas de uma query de usuários.
func collectUsers(rows pgx.Rows) ([]User, error) {
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

// listUsersByDomain devolve os usuários cujo email termina em @<domain>, do mais
// recente para o mais antigo.
func (s *Server) listUsersByDomain(ctx context.Context, domain string) ([]User, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+userCols+` FROM users WHERE email LIKE '%@' || $1 ORDER BY created_at DESC`,
		domain)
	if err != nil {
		return nil, err
	}
	return collectUsers(rows)
}

// listAllUsers devolve todos os usuários (qualquer domínio), do mais recente
// para o mais antigo.
func (s *Server) listAllUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return collectUsers(rows)
}

// updateUserAdmin atualiza nome, role e/ou quota. customRoleID só é aplicado quando role=4;
// quando role volta para 1/2/3, customRoleID é limpo automaticamente.
func (s *Server) updateUserAdmin(ctx context.Context, id int64, name *string, role *int16, quotaBytes *int64, customRoleID *string) (*User, error) {
	u, err := scanUser(s.db.QueryRow(ctx,
		`UPDATE users SET
		   name             = COALESCE($2, name),
		   role             = COALESCE($3, role),
		   quota_bytes      = COALESCE($4, quota_bytes),
		   custom_role_id   = CASE
		                        WHEN $3 = 4 THEN $5::uuid
		                        WHEN $3 IS NOT NULL AND $3 != 4 THEN NULL
		                        ELSE custom_role_id
		                      END
		 WHERE id = $1 RETURNING `+userCols,
		id, name, role, quotaBytes, customRoleID))
	if err == nil {
		s.invalidateUserCache(id)
	}
	return u, err
}

// setUserSuspended seta (now()) ou limpa (NULL) o suspended_at.
//
// Ao SUSPENDER, também revoga as credenciais em vigor para que a suspensão tenha
// efeito imediato (antes só gravava a coluna e o usuário seguia com acesso):
//   - apaga todas as sessões (refresh tokens) do usuário;
//   - expira todas as api_keys (PATs) ainda válidas — incluindo as eternas
//     (expires_at NULL) — setando expires_at=now(). Não usamos DELETE para
//     preservar o histórico/auditoria; userIDByAPIKeyHash passa a recusá-las.
//
// Tudo numa transação para não deixar estado meio-revogado. Ao DESUSPENDER, só
// limpa a coluna (as credenciais antigas continuam revogadas; o usuário gera novas).
func (s *Server) setUserSuspended(ctx context.Context, id int64, suspended bool) error {
	if !suspended {
		_, err := s.db.Exec(ctx, `UPDATE users SET suspended_at = NULL WHERE id = $1`, id)
		if err == nil {
			s.invalidateUserCache(id)
		}
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // no-op após Commit; garante rollback em qualquer erro
	if _, err := tx.Exec(ctx, `UPDATE users SET suspended_at = now() WHERE id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE api_keys SET expires_at = now()
		 WHERE user_id = $1 AND (expires_at IS NULL OR expires_at > now())`, id); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	// Invalida só após o commit: a suspensão precisa cortar o acesso de imediato
	// (resolveToken volta a checar suspended_at no banco em vez do cache velho).
	s.invalidateUserCache(id)
	return nil
}

// deleteUser remove o usuário (cascata em recovery_codes/api_keys via FK).
func (s *Server) deleteUser(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err == nil {
		s.invalidateUserCache(id)
	}
	return err
}

// updateAdminUserFull executa atomicamente (numa única transação) a atualização de
// senha + revogação de sessões (quando pwdHash não está vazio) e os campos de admin
// (nome, role, quota, customRoleID). Garante que nenhuma dessas etapas fique
// parcialmente aplicada em caso de erro intermediário.
func (s *Server) updateAdminUserFull(ctx context.Context, id int64, pwdHash string, name *string, role *int16, quotaBytes *int64, customRoleID *string) (*User, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if pwdHash != "" {
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id=$2`, pwdHash, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id); err != nil {
			return nil, err
		}
	}
	u, err := scanUser(tx.QueryRow(ctx,
		`UPDATE users SET
		   name           = COALESCE($2, name),
		   role           = COALESCE($3, role),
		   quota_bytes    = COALESCE($4, quota_bytes),
		   custom_role_id = CASE
		                      WHEN $3 = 4 THEN $5::uuid
		                      WHEN $3 IS NOT NULL AND $3 != 4 THEN NULL
		                      ELSE custom_role_id
		                    END
		 WHERE id = $1 RETURNING `+userCols,
		id, name, role, quotaBytes, customRoleID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.invalidateUserCache(id)
	return u, nil
}

func (s *Server) updatePassword(ctx context.Context, userID int64, hash string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id=$2`, hash, userID)
	if err == nil {
		s.invalidateUserCache(userID)
	}
	return err
}

func (s *Server) updateAvatarURL(ctx context.Context, userID int64, url string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET avatar_url=$1 WHERE id=$2`, url, userID)
	if err == nil {
		s.invalidateUserCache(userID)
	}
	return err
}

func (s *Server) setMFA(ctx context.Context, userID int64, enabled bool, secret *string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET mfa_enabled=$1, totp_secret=$2 WHERE id=$3`, enabled, secret, userID)
	if err == nil {
		s.invalidateUserCache(userID)
	}
	return err
}

func (s *Server) setEmailVerified(ctx context.Context, userID int64) error {
	// AND email_verified_at IS NULL torna a operação idempotente: re-verificar não
	// sobrescreve o timestamp original (relevante se o Del tiver sucesso mas o DB
	// falhar na primeira tentativa e o usuário repetir o fluxo via suporte).
	_, err := s.db.Exec(ctx, `UPDATE users SET email_verified_at=now() WHERE id=$1 AND email_verified_at IS NULL`, userID)
	if err == nil {
		s.invalidateUserCache(userID)
	}
	return err
}

// upsertPreferences faz merge parcial do objeto JSON `patch` no JSONB users.preferences
// (chaves novas entram, existentes são sobrescritas) e devolve o resultado final.
func (s *Server) upsertPreferences(ctx context.Context, userID int64, patch []byte) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.db.QueryRow(ctx,
		`UPDATE users SET preferences = preferences || $1::jsonb WHERE id=$2 RETURNING preferences`,
		string(patch), userID).Scan(&out)
	if err == nil {
		s.invalidateUserCache(userID)
	}
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
		EmailVerified: u.EmailVerifiedAt != nil,
		MFAMethod:     u.MFAMethod, MFATotp: u.HasTOTPSecret, MFAEmail: u.EmailVerifiedAt != nil,
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339), Preferences: prefs,
	}
	if u.SuspendedAt != nil {
		v := u.SuspendedAt.UTC().Format(time.RFC3339)
		p.SuspendedAt = &v
	}
	if u.Role == RoleCustom && u.CustomRoleID != nil {
		if cr, err := s.cachedCustomRole(ctx, *u.CustomRoleID); err != nil {
			slog.Error("falha ao carregar permissões do cargo customizado", "customRoleID", *u.CustomRoleID, "err", err)
		} else if cr != nil {
			p.Permissions = cr.Permissions
		}
	}
	return p
}

// ── Sessões (refresh tokens) ─────────────────────────────────────────────────

// createSession grava a sessão e devolve o id (usado no cookie "accounts").
func (s *Server) createSession(ctx context.Context, userID int64, refreshHash string, expires time.Time) (string, error) {
	var id string
	err := s.db.QueryRow(ctx,
		`INSERT INTO sessions (user_id, refresh_token_hash, expires_at) VALUES ($1,$2,$3) RETURNING id::text`,
		userID, refreshHash, expires).Scan(&id)
	return id, err
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

// sessionUserByID devolve o usuário de uma sessão VIVA (nil se expirada/inexistente).
func (s *Server) sessionUserByID(ctx context.Context, sessionID string) (*User, error) {
	return scanUser(s.db.QueryRow(ctx,
		`SELECT `+userCols2+` FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.id = $1::uuid AND s.expires_at > now()`, sessionID))
}

// AccountSummary é a visão de uma conta no chooser multi-conta.
type AccountSummary struct {
	SessionID string  `json:"sessionId"`
	Name      string  `json:"name"`
	Email     string  `json:"email"`
	AvatarURL *string `json:"avatarUrl"`
	Active    bool    `json:"active"`
}

// accountSummaries resolve os ids do cookie em contas vivas, preservando a
// ordem do cookie (sessões mortas simplesmente não voltam).
func (s *Server) accountSummaries(ctx context.Context, ids []string) ([]AccountSummary, error) {
	rows, err := s.db.Query(ctx,
		`SELECT s.id::text, u.name, u.email, u.avatar_url
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.id = ANY($1::uuid[]) AND s.expires_at > now() AND u.suspended_at IS NULL`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]AccountSummary{}
	for rows.Next() {
		var a AccountSummary
		if err := rows.Scan(&a.SessionID, &a.Name, &a.Email, &a.AvatarURL); err != nil {
			return nil, err
		}
		byID[a.SessionID] = a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]AccountSummary, 0, len(byID))
	for _, id := range ids {
		if a, ok := byID[id]; ok {
			out = append(out, a)
		}
	}
	return out, nil
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

// userIDByAPIKeyHash valida um PAT pelo hash: devolve o user_id se o token existe,
// não expirou E o dono está ativo (não suspenso, login habilitado), marcando o
// último uso. (0, nil) significa token inválido/expirado/dono inativo.
//
// O EXISTS contra users garante que suspender a conta (suspended_at) ou desabilitar
// o login (login_disabled) corta IMEDIATAMENTE o acesso via PAT, mesmo que o token
// tenha expires_at NULL (eterno). Sem isso um usuário suspenso seguiria autenticado.
func (s *Server) userIDByAPIKeyHash(ctx context.Context, hash string) (int64, error) {
	var userID int64
	err := s.db.QueryRow(ctx,
		`UPDATE api_keys SET last_used_at=now()
		 WHERE key_hash=$1 AND (expires_at IS NULL OR expires_at > now())
		   AND EXISTS (
		     SELECT 1 FROM users u
		     WHERE u.id = api_keys.user_id
		       AND u.suspended_at IS NULL
		       AND NOT u.login_disabled
		   )
		 RETURNING user_id`, hash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return userID, err
}

// ── OAuth clients (aplicações "Entrar com Santos Tech") ─────────────────────

type OAuthClient struct {
	ID           string    `json:"id"`
	ClientID     string    `json:"clientId"`
	Name         string    `json:"name"`
	RedirectURIs []string  `json:"redirectUris"`
	IsActive     bool      `json:"isActive"`
	CreatedAt    time.Time `json:"createdAt"`
}

const oauthClientCols = `id::text, client_id, name, redirect_uris, is_active, created_at`

func scanOAuthClient(row pgx.Row) (*OAuthClient, error) {
	var c OAuthClient
	err := row.Scan(&c.ID, &c.ClientID, &c.Name, &c.RedirectURIs, &c.IsActive, &c.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

func (s *Server) oauthClientByClientID(ctx context.Context, clientID string) (*OAuthClient, error) {
	return scanOAuthClient(s.db.QueryRow(ctx,
		`SELECT `+oauthClientCols+` FROM oauth_clients WHERE client_id=$1`, clientID))
}

func (s *Server) listOAuthClients(ctx context.Context) ([]OAuthClient, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+oauthClientCols+` FROM oauth_clients ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OAuthClient{}
	for rows.Next() {
		c, err := scanOAuthClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Server) insertOAuthClient(ctx context.Context, clientID, name string, uris []string) (*OAuthClient, error) {
	return scanOAuthClient(s.db.QueryRow(ctx,
		`INSERT INTO oauth_clients (client_id, name, redirect_uris) VALUES ($1,$2,$3)
		 RETURNING `+oauthClientCols, clientID, name, uris))
}

// updateOAuthClient atualiza campos não-nil (COALESCE, padrão updateUserAdmin).
func (s *Server) updateOAuthClient(ctx context.Context, id string, name *string, uris []string, active *bool) (*OAuthClient, error) {
	return scanOAuthClient(s.db.QueryRow(ctx,
		`UPDATE oauth_clients SET
		   name = COALESCE($2, name),
		   redirect_uris = COALESCE($3, redirect_uris),
		   is_active = COALESCE($4, is_active)
		 WHERE id = $1::uuid RETURNING `+oauthClientCols,
		id, name, uris, active))
}

func (s *Server) deleteOAuthClient(ctx context.Context, id string) (bool, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM oauth_clients WHERE id=$1::uuid`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ── Custom Roles ─────────────────────────────────────────────────────────────

type CustomRole struct {
	ID          string
	Name        string
	Description *string
	Permissions map[string][]string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (s *Server) listCustomRoles(ctx context.Context) ([]CustomRole, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id::text, name, description, permissions, created_at, updated_at
		 FROM custom_roles ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CustomRole
	for rows.Next() {
		var cr CustomRole
		var raw []byte
		if err := rows.Scan(&cr.ID, &cr.Name, &cr.Description, &raw, &cr.CreatedAt, &cr.UpdatedAt); err != nil {
			return nil, err
		}
		cr.Permissions = map[string][]string{}
		if err := json.Unmarshal(raw, &cr.Permissions); err != nil {
			return nil, fmt.Errorf("unmarshal permissions for role %s: %w", cr.ID, err)
		}
		out = append(out, cr)
	}
	return out, rows.Err()
}

func (s *Server) createCustomRole(ctx context.Context, name string, description *string, perms map[string][]string) (*CustomRole, error) {
	raw, err := json.Marshal(perms)
	if err != nil {
		return nil, fmt.Errorf("marshal permissions: %w", err)
	}
	var cr CustomRole
	var rawOut []byte
	err = s.db.QueryRow(ctx,
		`INSERT INTO custom_roles (name, description, permissions)
		 VALUES ($1, $2, $3::jsonb) RETURNING id::text, name, description, permissions, created_at, updated_at`,
		name, description, string(raw)).
		Scan(&cr.ID, &cr.Name, &cr.Description, &rawOut, &cr.CreatedAt, &cr.UpdatedAt)
	if err != nil {
		return nil, err
	}
	cr.Permissions = map[string][]string{}
	if err := json.Unmarshal(rawOut, &cr.Permissions); err != nil {
		return nil, fmt.Errorf("unmarshal permissions: %w", err)
	}
	return &cr, nil
}

func (s *Server) getCustomRole(ctx context.Context, id string) (*CustomRole, error) {
	var cr CustomRole
	var raw []byte
	err := s.db.QueryRow(ctx,
		`SELECT id::text, name, description, permissions, created_at, updated_at
		 FROM custom_roles WHERE id=$1::uuid`, id).
		Scan(&cr.ID, &cr.Name, &cr.Description, &raw, &cr.CreatedAt, &cr.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cr.Permissions = map[string][]string{}
	if err := json.Unmarshal(raw, &cr.Permissions); err != nil {
		return nil, fmt.Errorf("unmarshal permissions: %w", err)
	}
	return &cr, nil
}

func (s *Server) updateCustomRole(ctx context.Context, id, name string, description *string, perms map[string][]string) (*CustomRole, error) {
	raw, err := json.Marshal(perms)
	if err != nil {
		return nil, fmt.Errorf("marshal permissions: %w", err)
	}
	var cr CustomRole
	var rawOut []byte
	err = s.db.QueryRow(ctx,
		`UPDATE custom_roles SET name=$2, description=$3, permissions=$4::jsonb, updated_at=now()
		 WHERE id=$1::uuid RETURNING id::text, name, description, permissions, created_at, updated_at`,
		id, name, description, string(raw)).
		Scan(&cr.ID, &cr.Name, &cr.Description, &rawOut, &cr.CreatedAt, &cr.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cr.Permissions = map[string][]string{}
	if err := json.Unmarshal(rawOut, &cr.Permissions); err != nil {
		return nil, fmt.Errorf("unmarshal permissions: %w", err)
	}
	return &cr, nil
}

// deleteCustomRole remove o cargo. Retorna (true, nil) se deletou, (false, nil) se não existe,
// e (false, err) com code CARGO_IN_USE se há usuários vinculados.
func (s *Server) deleteCustomRole(ctx context.Context, id string) (bool, error) {
	var count int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE custom_role_id=$1::uuid`, id).Scan(&count); err != nil {
		return false, err
	}
	if count > 0 {
		return false, appErr(http.StatusConflict, "CARGO_IN_USE", "cargo está atribuído a um ou mais usuários")
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM custom_roles WHERE id=$1::uuid`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
