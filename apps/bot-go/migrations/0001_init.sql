-- ============================================================================
-- Migration #1 — Fundação do schema (F1)
-- ----------------------------------------------------------------------------
-- Estratégia de tenancy: shared schema + tenant_id + Row-Level Security (RLS)
-- desde a migração #1 (doc 4.1). RLS é o *backstop*: um filtro `WHERE` esquecido
-- vira no-op (zero linhas), nunca vazamento entre tenants.
--
-- Convenções (doc 7.8): toda tabela tem tenant_id NOT NULL, RLS habilitado +
-- FORCE, PK UUID, created_at timestamptz, índice com tenant_id na frente.
--
-- IMPORTANTE sobre o modelo de privilégios:
--   * O dono do banco (ex.: `bot`, superusuário criado pela imagem Postgres)
--     roda as MIGRAÇÕES, o PROVISIONAMENTO de tenants e o POLLER do outbox.
--     Superusuário IGNORA RLS — por isso essas operações são cross-tenant.
--   * Toda lógica de aplicação escopada a um tenant roda dentro de
--     `withTenant()` (ver tenant.ts), que faz `SET LOCAL ROLE bot_app` +
--     `SET LOCAL app.tenant_id`. `bot_app` é NOSUPERUSER NOBYPASSRLS → a RLS
--     é genuinamente aplicada. Sem isso, conectar como superusuário tornaria a
--     RLS inerte.
-- ============================================================================

-- gen_random_uuid() é nativo no PG13+, mas garantimos pgcrypto por segurança.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ----------------------------------------------------------------------------
-- Role de aplicação (escopo de RLS)
-- ----------------------------------------------------------------------------
-- NOINHERIT: não herda privilégios de roles das quais seja membro.
-- NOBYPASSRLS + NOSUPERUSER: garante que a RLS seja aplicada a esta role.
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'bot_app') THEN
    CREATE ROLE bot_app NOSUPERUSER NOBYPASSRLS NOINHERIT NOLOGIN;
  END IF;
END
$$;

-- Quem roda a migração (dono) precisa poder assumir bot_app via SET ROLE.
-- Superusuário já pode, mas isto mantém o `withTenant()` funcionando mesmo que
-- a app conecte com uma role não-superusuária no futuro.
GRANT bot_app TO CURRENT_USER;

-- ----------------------------------------------------------------------------
-- Helper de contexto de tenant (lido pelas políticas RLS)
-- ----------------------------------------------------------------------------
-- Lê o GUC custom `app.tenant_id` definido por `withTenant()` via SET LOCAL.
-- `missing_ok = true` + NULLIF('') → quando não setado retorna NULL, e a
-- comparação `tenant_id = NULL` é NULL (não TRUE) → fail-closed (zero linhas).
CREATE OR REPLACE FUNCTION current_tenant_id()
RETURNS uuid
LANGUAGE sql
STABLE
AS $$
  SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid
$$;

COMMENT ON FUNCTION current_tenant_id() IS
  'Tenant do contexto da transação (GUC app.tenant_id). NULL fora de withTenant() → RLS nega tudo (fail-closed).';

-- ----------------------------------------------------------------------------
-- Helper para aplicar RLS de forma uniforme (DRY → menos chance de esquecer)
-- ----------------------------------------------------------------------------
-- Habilita RLS + FORCE (aplica até ao dono da tabela) e cria a política padrão
-- de isolamento por tenant. Reutilizável por migrações futuras.
CREATE OR REPLACE FUNCTION app_apply_tenant_rls(tbl regclass, tenant_col text DEFAULT 'tenant_id')
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
  EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', tbl);
  EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', tbl);
  EXECUTE format(
    'CREATE POLICY tenant_isolation ON %s USING (%I = current_tenant_id()) WITH CHECK (%I = current_tenant_id())',
    tbl, tenant_col, tenant_col
  );
  -- Acopla o GRANT à RLS no MESMO passo: bot_app só ganha DML numa tabela junto
  -- com ENABLE+FORCE RLS. Consequência (fail-safe): se uma migração futura criar
  -- uma tabela e ESQUECER esta chamada, bot_app não recebe privilégio nenhum →
  -- "permission denied" (falha barulhenta), nunca um vazamento cross-tenant
  -- silencioso. Não há ALTER DEFAULT PRIVILEGES global justamente para que
  -- privilégio e RLS nunca divirjam.
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %s TO bot_app', tbl);
END
$$;

COMMENT ON FUNCTION app_apply_tenant_rls(regclass, text) IS
  'Habilita RLS + FORCE, cria a política tenant_isolation e concede DML a bot_app (acoplados). Use em TODA tabela escopada a tenant.';

-- ----------------------------------------------------------------------------
-- Tipos enumerados (espelham os enums Zod de @bot/shared)
-- ----------------------------------------------------------------------------
-- Canal de atendimento. Novo canal (Instagram, etc.) = ALTER TYPE channel
-- ADD VALUE — nunca ALTER em tabela existente (doc 2.4 / 3.1).
CREATE TYPE channel AS ENUM ('whatsapp');

-- FSM da conversa (doc 2.7). Inclui os estados gated reservados para F2.
CREATE TYPE conversation_state AS ENUM (
  'NEW',
  'ENGAGED',
  'AWAITING_REPLY',
  'FOLLOWUP_PENDING',
  'CONCLUDED_POSITIVE',
  'CONCLUDED_NEGATIVE',
  'CONCLUDED_NO_ANSWER',
  'AWAITING_TOOL_CONFIRMATION',
  'AWAITING_HUMAN_APPROVAL',
  'HANDOFF',
  'DORMANT',
  'REACTIVATION_SCHEDULED'
);

-- Modalidade de resposta (espelhamento de mídia, doc 2.10).
CREATE TYPE reply_modality AS ENUM ('text', 'audio');

-- Consentimento append-only (doc 2.13).
CREATE TYPE consent_status AS ENUM ('opt_in', 'opt_out');

-- Intenção da mensagem de saída (doc 6.3 / shared OutboundIntent).
CREATE TYPE outbound_intent AS ENUM (
  'FREE_FORM',
  'STRUCTURED_REENGAGEMENT',
  'TRANSACTIONAL_UPDATE'
);

-- Status de entrega de mensagem (shared MessageDeliveryStatus).
CREATE TYPE message_delivery_status AS ENUM (
  'pending',
  'sent',
  'delivered',
  'read',
  'failed',
  'deleted'
);

-- Status de processamento de webhook genérico (doc 4.6.2).
CREATE TYPE webhook_processing_status AS ENUM (
  'pending',
  'processing',
  'done',
  'failed'
);

-- Status de um contato agendado / follow-up (doc 2.5, poller).
CREATE TYPE scheduled_status AS ENUM (
  'pending',
  'processing',
  'fired',
  'cancelled',
  'failed'
);

-- Notificações desacopladas (doc 4.6.4 / shared).
CREATE TYPE notification_audience AS ENUM ('client', 'admin');
CREATE TYPE notification_channel AS ENUM ('email', 'whatsapp');
CREATE TYPE notification_type AS ENUM (
  'KB_GAP',
  'AUDIO_GAP',
  'PAYMENT',
  'TAKEOVER',
  'ERROR'
);
CREATE TYPE notification_status AS ENUM (
  'pending',
  'sent',
  'failed',
  'suppressed'
);

-- ============================================================================
-- TABELAS
-- ============================================================================

-- ----------------------------------------------------------------------------
-- tenants — âncora de tenancy. É a ÚNICA tabela cujo "tenant" é o próprio `id`
-- (não há coluna tenant_id redundante). A política RLS isola por `id`.
-- CRUD de tenants é operação de ADMIN (conexão dono), não de bot_app.
-- ----------------------------------------------------------------------------
CREATE TABLE tenants (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name        text NOT NULL,
  slug        text NOT NULL UNIQUE,
  -- Fuso horário IANA do tenant (doc 2.5 / 5-F1). Fonte única da verdade de tz.
  timezone    text NOT NULL DEFAULT 'America/Sao_Paulo',
  status      text NOT NULL DEFAULT 'active',
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenants
  USING (id = current_tenant_id())
  WITH CHECK (id = current_tenant_id());

-- tenants usa RLS manual (coluna `id`), então o grant é manual aqui. bot_app só
-- lê e atualiza a PRÓPRIA linha (sob RLS); criar/excluir tenant é do dono
-- (provisionamento/teardown), não da app.
GRANT SELECT, UPDATE ON tenants TO bot_app;

COMMENT ON TABLE tenants IS 'Empresa/bot na plataforma. RLS isola por id (não por tenant_id).';

-- ----------------------------------------------------------------------------
-- contact — o ser humano. SEM telefone. Âncora de tudo que pertence à pessoa
-- (doc 2.4). Nova capacidade = nova tabela com FK para contact.id.
-- ----------------------------------------------------------------------------
CREATE TABLE contact (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  display_name        text,
  structured_facts    jsonb NOT NULL DEFAULT '{}'::jsonb,
  global_suppression  boolean NOT NULL DEFAULT false,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  -- Necessário para FKs compostas (tenant_id, contact_id) dos filhos.
  UNIQUE (tenant_id, id)
);

CREATE INDEX idx_contact_tenant ON contact (tenant_id);
SELECT app_apply_tenant_rls('contact');

COMMENT ON TABLE contact IS 'Humano (raiz de identidade). Sem telefone — canais vivem em channel_identity.';

-- ----------------------------------------------------------------------------
-- channel_identity — identidade da pessoa em um canal específico. O telefone
-- WhatsApp vive aqui como external_id. Novo canal = INSERT (doc 2.4 / 7.3).
-- ----------------------------------------------------------------------------
CREATE TABLE channel_identity (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  contact_id      uuid NOT NULL,
  channel         channel NOT NULL,
  external_id     text NOT NULL,
  display_handle  text,
  raw_profile     jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  -- Lookup de roteamento de inbound + dedup por canal.
  UNIQUE (tenant_id, channel, external_id),
  -- FK composta garante que o contact é do MESMO tenant (isolamento no DB).
  FOREIGN KEY (tenant_id, contact_id)
    REFERENCES contact (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_channel_identity_contact ON channel_identity (tenant_id, contact_id);
SELECT app_apply_tenant_rls('channel_identity');

COMMENT ON TABLE channel_identity IS 'Identidade por canal (telefone WhatsApp = external_id). Novo canal = INSERT.';

-- ----------------------------------------------------------------------------
-- conversation — thread por canal de um contact (doc 2.7). Carrega o estado FSM,
-- janela, takeover. Uma por channel_identity.
-- ----------------------------------------------------------------------------
CREATE TABLE conversation (
  id                        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                 uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  channel_identity_id       uuid NOT NULL,
  channel                   channel NOT NULL,
  state                     conversation_state NOT NULL DEFAULT 'NEW',
  last_inbound_at           timestamptz,
  last_outbound_at          timestamptz,
  last_inbound_modality     reply_modality,
  chatwoot_conversation_id  bigint,
  bot_enabled               boolean NOT NULL DEFAULT true,
  prompt_template_version   text,
  created_at                timestamptz NOT NULL DEFAULT now(),
  updated_at                timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  -- Uma conversa por canal-thread (doc 2.7: "uma por channel_identity").
  UNIQUE (tenant_id, channel_identity_id),
  FOREIGN KEY (tenant_id, channel_identity_id)
    REFERENCES channel_identity (tenant_id, id) ON DELETE CASCADE
);

-- Índice parcial p/ o motor de follow-up (doc 11): tenant, state, last_inbound_at.
CREATE INDEX idx_conversation_active
  ON conversation (tenant_id, state, last_inbound_at)
  WHERE state IN ('ENGAGED', 'AWAITING_REPLY', 'FOLLOWUP_PENDING');
SELECT app_apply_tenant_rls('conversation');

COMMENT ON TABLE conversation IS 'Thread por canal de um contact. Inbox unificada = agrupar por contact_id.';

-- ----------------------------------------------------------------------------
-- consent — opt-in/opt-out append-only, por channel_identity (doc 2.13).
-- STOP/bloqueio = nova linha opt_out (nunca UPDATE → trilha de auditoria).
-- ----------------------------------------------------------------------------
CREATE TABLE consent (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  contact_id           uuid NOT NULL,
  channel_identity_id  uuid NOT NULL,
  status               consent_status NOT NULL,
  text                 text,
  source               text NOT NULL,
  occurred_at          timestamptz NOT NULL DEFAULT now(),
  created_at           timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, contact_id)
    REFERENCES contact (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, channel_identity_id)
    REFERENCES channel_identity (tenant_id, id) ON DELETE CASCADE
);

-- "Último consentimento do canal" = primeira linha por occurred_at DESC.
CREATE INDEX idx_consent_latest
  ON consent (tenant_id, channel_identity_id, occurred_at DESC);
SELECT app_apply_tenant_rls('consent');

COMMENT ON TABLE consent IS 'Trilha append-only de opt-in/opt-out por canal. Nunca UPDATE in-place.';

-- ----------------------------------------------------------------------------
-- domain_events — OUTBOX transacional (doc 4.6.1). Escrito na MESMA transação
-- da mudança de estado (ver withTenant/emit). O poller (dono) drena → pub/sub.
-- event_type é TEXT (catálogo extensível em @bot/shared valida no app).
-- ----------------------------------------------------------------------------
CREATE TABLE domain_events (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  event_type    text NOT NULL,
  aggregate_id  text NOT NULL,
  payload       jsonb NOT NULL DEFAULT '{}'::jsonb,
  occurred_at   timestamptz NOT NULL DEFAULT now(),
  processed_at  timestamptz,
  attempts      integer NOT NULL DEFAULT 0,
  last_error    text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id)
);

-- Índice do poller: drenar não processados em ordem (drain global do dono).
CREATE INDEX idx_domain_events_unprocessed
  ON domain_events (occurred_at)
  WHERE processed_at IS NULL;
SELECT app_apply_tenant_rls('domain_events');

COMMENT ON TABLE domain_events IS 'Outbox transacional. Núcleo EMITE; poller drena para o event bus in-process.';

-- ----------------------------------------------------------------------------
-- processed_events — idempotência de consumidor (doc 4.6.1).
-- ----------------------------------------------------------------------------
CREATE TABLE processed_events (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  subscriber_name  text NOT NULL,
  event_id         uuid NOT NULL,
  processed_at     timestamptz NOT NULL DEFAULT now(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  -- Idempotência: um subscriber processa cada evento no máximo uma vez.
  UNIQUE (subscriber_name, event_id),
  FOREIGN KEY (tenant_id, event_id)
    REFERENCES domain_events (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_processed_events_tenant ON processed_events (tenant_id);
SELECT app_apply_tenant_rls('processed_events');

COMMENT ON TABLE processed_events IS 'Ledger de idempotência (subscriber_name, event_id).';

-- ----------------------------------------------------------------------------
-- webhook_events — log genérico de webhooks (doc 4.6.2). provider_event_id
-- UNIQUE = dedup; raw_body retém os bytes exatos para HMAC de pagamentos (F5).
-- ----------------------------------------------------------------------------
CREATE TABLE webhook_events (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  provider            text NOT NULL,
  provider_event_id   text,
  raw_payload         jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- Bytes exatos capturados ANTES do parse de JSON (doc 4.6.2): habilita
  -- re-verificação HMAC de pagamentos no F5. NULL quando não retido.
  raw_body            bytea,
  signature_verified  boolean NOT NULL DEFAULT false,
  processing_status   webhook_processing_status NOT NULL DEFAULT 'pending',
  received_at         timestamptz NOT NULL DEFAULT now(),
  created_at          timestamptz NOT NULL DEFAULT now(),
  -- Dedup ESCOPADA A TENANT. O roteador é /webhooks/:provider/:tenantId, então
  -- a dedup é por tenant — e a unicidade no Postgres é checada ABAIXO da RLS,
  -- então uma chave global vazaria a existência de eventos de outro tenant
  -- (unique_violation cross-tenant) e poderia descartar webhook legítimo.
  -- Prefixar tenant_id elimina esse canal lateral e satisfaz a convenção 7.8
  -- (índice com tenant_id na frente). NULLs continuam não-conflitando.
  UNIQUE (tenant_id, provider, provider_event_id)
);

CREATE INDEX idx_webhook_events_pending
  ON webhook_events (received_at)
  WHERE processing_status = 'pending';
SELECT app_apply_tenant_rls('webhook_events');

COMMENT ON TABLE webhook_events IS 'Log genérico de webhooks. raw_body (bytea) p/ HMAC; dedup (tenant_id, provider, provider_event_id).';

-- ----------------------------------------------------------------------------
-- outbound_message — envio exactly-once (doc 11). idempotency_key UNIQUE.
-- ----------------------------------------------------------------------------
CREATE TABLE outbound_message (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  conversation_id      uuid NOT NULL,
  provider_message_id  text,
  idempotency_key      text NOT NULL,
  status               message_delivery_status NOT NULL DEFAULT 'pending',
  intent_category      outbound_intent NOT NULL,
  content              jsonb,
  template_payload     jsonb,
  error                jsonb,
  sent_at              timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  -- Exactly-once: a chave de idempotência é única por tenant.
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, conversation_id)
    REFERENCES conversation (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_outbound_message_conversation
  ON outbound_message (tenant_id, conversation_id);
-- Lookup por provider_message_id em callbacks de status de entrega.
CREATE INDEX idx_outbound_message_provider
  ON outbound_message (tenant_id, provider_message_id)
  WHERE provider_message_id IS NOT NULL;
SELECT app_apply_tenant_rls('outbound_message');

COMMENT ON TABLE outbound_message IS 'Envio exactly-once. idempotency_key UNIQUE por tenant.';

-- ----------------------------------------------------------------------------
-- tenant_features — feature flags por tenant (doc 4.6.3). PK composta (não UUID):
-- é uma matriz de chave→estado, upsert por (tenant_id, feature_key).
-- ----------------------------------------------------------------------------
CREATE TABLE tenant_features (
  tenant_id    uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  feature_key  text NOT NULL,
  enabled      boolean NOT NULL DEFAULT false,
  config       jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, feature_key)
);

SELECT app_apply_tenant_rls('tenant_features');

COMMENT ON TABLE tenant_features IS 'Gate de capacidade por tenant. Seed: core_messaging=on, audio/calendar/payments=off.';

-- ----------------------------------------------------------------------------
-- notification_settings — matriz de toggle (doc 4.6.4). PK 4-tupla; tipo novo
-- de notificação = INSERT, não edição de código.
-- ----------------------------------------------------------------------------
CREATE TABLE notification_settings (
  tenant_id          uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  type               notification_type NOT NULL,
  audience           notification_audience NOT NULL,
  channel            notification_channel NOT NULL,
  enabled            boolean NOT NULL DEFAULT true,
  delivery_strategy  jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, type, audience, channel)
);

SELECT app_apply_tenant_rls('notification_settings');

COMMENT ON TABLE notification_settings IS 'Matriz (tipo × audiência × canal) → enabled. Linha nova = INSERT.';

-- ----------------------------------------------------------------------------
-- tenant_contacts — destinatários admin para notificações (doc 11).
-- ----------------------------------------------------------------------------
CREATE TABLE tenant_contacts (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  name             text NOT NULL,
  email            text,
  whatsapp_number  text,
  quiet_hours      jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_tenant_contacts_tenant ON tenant_contacts (tenant_id);
SELECT app_apply_tenant_rls('tenant_contacts');

COMMENT ON TABLE tenant_contacts IS 'Contatos de admin (email/WhatsApp) para o ponto único de notificação.';

-- ----------------------------------------------------------------------------
-- notifications — log de notificações (doc 11). dedup_key UNIQUE; serve dedup,
-- view e auditoria LGPD.
-- ----------------------------------------------------------------------------
CREATE TABLE notifications (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  type                 notification_type NOT NULL,
  audience             notification_audience NOT NULL,
  channel              notification_channel NOT NULL,
  recipient_ref        text,
  dedup_key            text NOT NULL,
  status               notification_status NOT NULL DEFAULT 'pending',
  provider_message_id  text,
  payload              jsonb NOT NULL DEFAULT '{}'::jsonb,
  error                text,
  sent_at              timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, dedup_key)
);

CREATE INDEX idx_notifications_status ON notifications (tenant_id, type, status);
SELECT app_apply_tenant_rls('notifications');

COMMENT ON TABLE notifications IS 'Log de notificações. dedup_key UNIQUE por tenant (dedup + auditoria).';

-- ----------------------------------------------------------------------------
-- lead — camada CRM sobre contact (doc 2.4). NÃO é a raiz da identidade.
-- ----------------------------------------------------------------------------
CREATE TABLE lead (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  contact_id  uuid NOT NULL,
  status      text NOT NULL DEFAULT 'new',
  interest    text,
  owner       text,
  email       text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  -- Um overlay de lead por contact.
  UNIQUE (tenant_id, contact_id),
  FOREIGN KEY (tenant_id, contact_id)
    REFERENCES contact (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_lead_status ON lead (tenant_id, status);
SELECT app_apply_tenant_rls('lead');

COMMENT ON TABLE lead IS 'Overlay CRM (status/interesse/dono/email) sobre contact.';

-- ----------------------------------------------------------------------------
-- lead_summary — resumo rolling em linguagem natural por contact (doc 2.4).
-- ----------------------------------------------------------------------------
CREATE TABLE lead_summary (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  contact_id  uuid NOT NULL,
  summary     text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  -- Um resumo por contact (atualizado in-place).
  UNIQUE (tenant_id, contact_id),
  FOREIGN KEY (tenant_id, contact_id)
    REFERENCES contact (tenant_id, id) ON DELETE CASCADE
);

SELECT app_apply_tenant_rls('lead_summary');

COMMENT ON TABLE lead_summary IS 'Resumo rolling por contact. Injetado ao retomar conversa (não o transcript inteiro).';

-- ----------------------------------------------------------------------------
-- scheduled_contacts — reativações/follow-ups (doc 2.5). Postgres é a fonte da
-- verdade (não Redis); drenado pelo poller.
-- ----------------------------------------------------------------------------
CREATE TABLE scheduled_contacts (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  contact_id       uuid NOT NULL,
  conversation_id  uuid,
  fire_at          timestamptz NOT NULL,
  status           scheduled_status NOT NULL DEFAULT 'pending',
  payload          jsonb NOT NULL DEFAULT '{}'::jsonb,
  attempts         integer NOT NULL DEFAULT 0,
  last_error       text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, contact_id)
    REFERENCES contact (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, conversation_id)
    REFERENCES conversation (tenant_id, id) ON DELETE CASCADE
);

-- Índice do poller: itens vencidos pendentes (drain global do dono).
CREATE INDEX idx_scheduled_due
  ON scheduled_contacts (fire_at)
  WHERE status = 'pending';
-- Índice tenant-first (convenção 7.8): listagem por tenant (painel 2.11) e
-- CASCADE de delete de tenant sem seq scan.
CREATE INDEX idx_scheduled_contacts_tenant
  ON scheduled_contacts (tenant_id, status, fire_at);
SELECT app_apply_tenant_rls('scheduled_contacts');

COMMENT ON TABLE scheduled_contacts IS 'Reativações/follow-ups agendados. fire_at no fuso resolvido do tenant.';

-- ----------------------------------------------------------------------------
-- tenant_config — config hot do bot em colunas estritas (doc 11). O timezone é
-- canônico em tenants (doc 2.5), não duplicado aqui.
-- ----------------------------------------------------------------------------
CREATE TABLE tenant_config (
  id                        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                 uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  -- Persona (doc 2.2)
  bot_name                  text NOT NULL DEFAULT 'Emilly',
  bot_gender                text NOT NULL DEFAULT 'feminino',
  -- Velocidade de resposta — simulação humana (doc 2.3), em milissegundos
  min_initial_ms            integer NOT NULL DEFAULT 2000,
  max_initial_ms            integer NOT NULL DEFAULT 6000,
  min_active_ms             integer NOT NULL DEFAULT 800,
  max_active_ms             integer NOT NULL DEFAULT 3000,
  inactivity_reset_minutes  integer NOT NULL DEFAULT 20,
  debounce_ms               integer NOT NULL DEFAULT 1500,
  quiet_hours               jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- Horários estratégicos de follow-up (doc 2.6), rotacionam por tentativa
  strategic_hours           integer[] NOT NULL DEFAULT '{8,10,12,13,14,17,18,19}',
  -- Extensões de persona não-hot (estilo default, etc.)
  persona                   jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at                timestamptz NOT NULL DEFAULT now(),
  updated_at                timestamptz NOT NULL DEFAULT now(),
  -- Uma config por tenant.
  UNIQUE (tenant_id)
);

SELECT app_apply_tenant_rls('tenant_config');

COMMENT ON TABLE tenant_config IS 'Config hot do bot (persona, timing, quiet hours, horários). tz vive em tenants.';

-- ============================================================================
-- GRANTS para a role de aplicação (bot_app)
-- ----------------------------------------------------------------------------
-- bot_app só precisa de USAGE no schema. O DML por tabela é concedido de forma
-- ACOPLADA à RLS dentro de app_apply_tenant_rls() (e manualmente em `tenants`).
--
-- DELIBERADAMENTE NÃO usamos `GRANT ... ON ALL TABLES` nem `ALTER DEFAULT
-- PRIVILEGES`: um grant cego tornaria a RLS opt-out-por-omissão — uma tabela
-- futura que esquecesse a RLS já teria DML garantido para bot_app (vazamento
-- silencioso). Acoplando grant↔RLS, esquecer a RLS = sem privilégio = falha
-- barulhenta. A invariante "toda tabela acessível a bot_app tem RLS+FORCE" é
-- checada no smoke test (verify.mjs, teste [9]).
-- ============================================================================
GRANT USAGE ON SCHEMA public TO bot_app;
