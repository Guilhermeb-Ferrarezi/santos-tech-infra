-- Consolidated schema for sqlc (final state after all 30 migrations).
-- Stripped of: RLS policies, DO $$ blocks, GRANTs, functions, comments.
-- Only tables and types actually used in queries.

-- ============================================================================
-- Enums
-- ============================================================================

CREATE TYPE channel AS ENUM ('whatsapp', 'evolution');

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

CREATE TYPE reply_modality AS ENUM ('text', 'audio');

CREATE TYPE outbound_intent AS ENUM (
  'FREE_FORM',
  'STRUCTURED_REENGAGEMENT',
  'TRANSACTIONAL_UPDATE'
);

CREATE TYPE message_delivery_status AS ENUM (
  'pending',
  'sent',
  'delivered',
  'read',
  'failed',
  'deleted'
);

CREATE TYPE webhook_processing_status AS ENUM (
  'pending',
  'processing',
  'done',
  'failed'
);

CREATE TYPE scheduled_status AS ENUM (
  'pending',
  'processing',
  'fired',
  'cancelled',
  'failed',
  'exhausted',
  'skipped'
);

-- ============================================================================
-- Tables
-- ============================================================================

CREATE TABLE tenants (
  id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  name       text        NOT NULL,
  slug       text        NOT NULL UNIQUE,
  timezone   text        NOT NULL DEFAULT 'America/Sao_Paulo',
  status     text        NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE contact (
  id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  display_name        text,
  communication_style text,
  structured_facts    jsonb       NOT NULL DEFAULT '{}'::jsonb,
  global_suppression  boolean     NOT NULL DEFAULT false,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id)
);

CREATE TABLE channel_identity (
  id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  contact_id     uuid        NOT NULL,
  channel        channel     NOT NULL,
  external_id    text        NOT NULL,
  display_handle text,
  raw_profile    jsonb       NOT NULL DEFAULT '{}'::jsonb,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, channel, external_id),
  FOREIGN KEY (tenant_id, contact_id)
    REFERENCES contact (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE conversation (
  id                       uuid                NOT NULL DEFAULT gen_random_uuid(),
  tenant_id                uuid                NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  channel_identity_id      uuid                NOT NULL,
  channel                  channel             NOT NULL,
  state                    conversation_state  NOT NULL DEFAULT 'NEW',
  last_inbound_at          timestamptz,
  last_outbound_at         timestamptz,
  last_inbound_modality    reply_modality,
  chatwoot_conversation_id bigint,
  bot_enabled              boolean             NOT NULL DEFAULT true,
  prompt_template_version  text,
  created_at               timestamptz         NOT NULL DEFAULT now(),
  updated_at               timestamptz         NOT NULL DEFAULT now(),
  PRIMARY KEY (id),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, channel_identity_id),
  FOREIGN KEY (tenant_id, channel_identity_id)
    REFERENCES channel_identity (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE domain_events (
  id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  event_type   text        NOT NULL,
  aggregate_id text        NOT NULL,
  payload      jsonb       NOT NULL DEFAULT '{}'::jsonb,
  occurred_at  timestamptz NOT NULL DEFAULT now(),
  processed_at timestamptz,
  attempts     integer     NOT NULL DEFAULT 0,
  last_error   text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id)
);

CREATE TABLE webhook_events (
  id                 uuid                       PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid                       NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  provider           text                       NOT NULL,
  provider_event_id  text,
  raw_payload        jsonb                      NOT NULL DEFAULT '{}'::jsonb,
  raw_body           bytea,
  signature_verified boolean                    NOT NULL DEFAULT false,
  processing_status  webhook_processing_status  NOT NULL DEFAULT 'pending',
  received_at        timestamptz                NOT NULL DEFAULT now(),
  processed_at       timestamptz,
  last_error         text,
  attempts           integer                    NOT NULL DEFAULT 0,
  next_retry_at      timestamptz,
  created_at         timestamptz                NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, provider, provider_event_id)
);

CREATE TABLE outbound_message (
  id                  uuid                    PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid                    NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  conversation_id     uuid                    NOT NULL,
  provider_message_id text,
  idempotency_key     text                    NOT NULL,
  status              message_delivery_status NOT NULL DEFAULT 'pending',
  intent_category     outbound_intent         NOT NULL,
  content             jsonb,
  template_payload    jsonb,
  error               jsonb,
  sent_at             timestamptz,
  reasoning           jsonb,
  created_at          timestamptz             NOT NULL DEFAULT now(),
  updated_at          timestamptz             NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, conversation_id)
    REFERENCES conversation (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE inbound_message (
  id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  conversation_id     uuid        NOT NULL,
  provider_message_id text        NOT NULL,
  content             jsonb       NOT NULL,
  received_at         timestamptz NOT NULL,
  created_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, provider_message_id),
  FOREIGN KEY (tenant_id, conversation_id)
    REFERENCES conversation (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE lead (
  id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  contact_id  uuid        NOT NULL,
  status      text        NOT NULL DEFAULT 'new',
  interest    text,
  owner       text,
  email       text,
  origin      text        NOT NULL DEFAULT 'oficial',
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, contact_id),
  FOREIGN KEY (tenant_id, contact_id)
    REFERENCES contact (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE lead_summary (
  id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  contact_id  uuid        NOT NULL,
  summary     text        NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, contact_id),
  FOREIGN KEY (tenant_id, contact_id)
    REFERENCES contact (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE scheduled_contacts (
  id               uuid             PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid             NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  contact_id       uuid             NOT NULL,
  conversation_id  uuid,
  fire_at          timestamptz      NOT NULL,
  status           scheduled_status NOT NULL DEFAULT 'pending',
  payload          jsonb            NOT NULL DEFAULT '{}'::jsonb,
  attempts         integer          NOT NULL DEFAULT 0,
  last_error       text,
  max_attempts     integer          NOT NULL DEFAULT 3,
  last_attempted_at timestamptz,
  sent_at          timestamptz,
  created_at       timestamptz      NOT NULL DEFAULT now(),
  updated_at       timestamptz      NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, contact_id)
    REFERENCES contact (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, conversation_id)
    REFERENCES conversation (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE tenant_config (
  id                              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                       uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  bot_name                        text        NOT NULL DEFAULT 'Emilly',
  bot_gender                      text        NOT NULL DEFAULT 'feminino',
  min_initial_ms                  integer     NOT NULL DEFAULT 2000,
  max_initial_ms                  integer     NOT NULL DEFAULT 6000,
  min_active_ms                   integer     NOT NULL DEFAULT 800,
  max_active_ms                   integer     NOT NULL DEFAULT 3000,
  inactivity_reset_minutes        integer     NOT NULL DEFAULT 20,
  debounce_ms                     integer     NOT NULL DEFAULT 1500,
  quiet_hours                     jsonb       NOT NULL DEFAULT '{}'::jsonb,
  strategic_hours                 integer[]   NOT NULL DEFAULT '{8,10,12,13,14,17,18,19}',
  persona                         jsonb       NOT NULL DEFAULT '{}'::jsonb,
  kb_content                      jsonb       NOT NULL DEFAULT '[]'::jsonb,
  system_prompt                   text        NOT NULL DEFAULT '',
  admin_system_prompt             text        NOT NULL DEFAULT '',
  bot_enabled_by_default          boolean     NOT NULL DEFAULT true,
  bot_allowed_numbers             jsonb       NOT NULL DEFAULT '[]'::jsonb,
  admin_whatsapp_number           text        NOT NULL DEFAULT '',
  admin_whatsapp_numbers          jsonb       NOT NULL DEFAULT '[]'::jsonb,
  evolution_bot_reply_enabled     boolean     NOT NULL DEFAULT false,
  evolution_lead_capture_enabled  boolean     NOT NULL DEFAULT true,
  evolution_capture_disabled      jsonb       NOT NULL DEFAULT '[]'::jsonb,
  notif_phone                     text        NOT NULL DEFAULT '',
  notif_instance                  text        NOT NULL DEFAULT '',
  notif_enabled                   boolean     NOT NULL DEFAULT false,
  notif_on_success                boolean     NOT NULL DEFAULT false,
  notif_on_container_down         boolean     NOT NULL DEFAULT true,
  created_at                      timestamptz NOT NULL DEFAULT now(),
  updated_at                      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id)
);

CREATE TABLE bot_processing_log (
  id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        text        NOT NULL,
  kind             text        NOT NULL DEFAULT 'message',
  conversation_id  uuid        REFERENCES conversation (id) ON DELETE SET NULL,
  contact_phone    text        NOT NULL DEFAULT '',
  contact_name     text        NOT NULL DEFAULT '',
  inbound_text     text        NOT NULL DEFAULT '',
  answered         boolean,
  answered_from_kb boolean,
  handoff          boolean,
  cited_entry_ids  jsonb,
  bubbles          jsonb,
  tool_calls       jsonb,
  processing_ms    integer     NOT NULL DEFAULT 0,
  error            text,
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE pending_question (
  id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  conversation_id  uuid        NOT NULL,
  client_phone     text        NOT NULL,
  client_name      text        NOT NULL DEFAULT '',
  question         text        NOT NULL,
  status           text        NOT NULL DEFAULT 'open',
  draft            text,
  channel          text        NOT NULL DEFAULT 'whatsapp',
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, conversation_id)
    REFERENCES conversation (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE pending_booking (
  id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  conversation_id  uuid        NOT NULL,
  client_phone     text        NOT NULL,
  client_name      text        NOT NULL DEFAULT '',
  student_name     text        NOT NULL DEFAULT '',
  kind             text        NOT NULL DEFAULT 'experimental',
  course           text        NOT NULL DEFAULT '',
  proposed_day     text        NOT NULL DEFAULT '',
  proposed_date    text        NOT NULL DEFAULT '',
  proposed_time    text        NOT NULL DEFAULT '',
  proposed_period  text        NOT NULL DEFAULT '',
  age              integer,
  notes            text        NOT NULL DEFAULT '',
  channel          text        NOT NULL DEFAULT 'whatsapp',
  status           text        NOT NULL DEFAULT 'open',
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, conversation_id)
    REFERENCES conversation (tenant_id, id) ON DELETE CASCADE
);
