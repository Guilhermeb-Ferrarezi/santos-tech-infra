-- ============================================================================
-- Migration #18 — pending_booking (agendamentos aguardando confirmação do admin)
-- ----------------------------------------------------------------------------
-- O cliente pede para agendar (aula experimental ou individual); o bot propõe um
-- horário e grava aqui (status 'open'). O admin confirma → o bot grava a aula no
-- Notion e avisa o cliente (status 'confirmed'); ou rejeita ('rejected').
-- Escopada por tenant (RLS via app_apply_tenant_rls).
-- ============================================================================
CREATE TABLE pending_booking (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  conversation_id  uuid NOT NULL,
  client_phone     text NOT NULL,
  client_name      text NOT NULL DEFAULT '',
  kind             text NOT NULL DEFAULT 'experimental',  -- experimental | individual
  course           text NOT NULL DEFAULT '',
  proposed_day     text NOT NULL DEFAULT '',
  proposed_time    text NOT NULL DEFAULT '',
  proposed_period  text NOT NULL DEFAULT '',
  age              integer,
  notes            text NOT NULL DEFAULT '',
  status           text NOT NULL DEFAULT 'open',           -- open | confirmed | rejected
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, conversation_id)
    REFERENCES conversation (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_pending_booking_open
  ON pending_booking (tenant_id, status)
  WHERE status = 'open';

SELECT app_apply_tenant_rls('pending_booking');

COMMENT ON TABLE pending_booking IS 'Agendamentos de aula propostos pelo bot, aguardando confirmação do admin.';
