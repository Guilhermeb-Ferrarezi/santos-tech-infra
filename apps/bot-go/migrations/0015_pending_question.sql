-- ============================================================================
-- Migration #15 — pending_question (fila de dúvidas de clientes aguardando)
-- ----------------------------------------------------------------------------
-- Quando o bot dá handoff (não soube responder um cliente), grava aqui a dúvida.
-- O admin, ao fornecer a informação no modo admin, "casa" com a pendência, o bot
-- rascunha a resposta (status='drafted'), o admin confirma e o bot envia ao
-- cliente (status='resolved'). Escopada por tenant (RLS via app_apply_tenant_rls).
-- ============================================================================
CREATE TABLE pending_question (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  conversation_id  uuid NOT NULL,
  client_phone     text NOT NULL,
  client_name      text NOT NULL DEFAULT '',
  question         text NOT NULL,
  status           text NOT NULL DEFAULT 'open',  -- open | drafted | resolved
  draft            text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, conversation_id)
    REFERENCES conversation (tenant_id, id) ON DELETE CASCADE
);

-- Índice do casamento: pendências abertas/rascunhadas por tenant.
CREATE INDEX idx_pending_question_open
  ON pending_question (tenant_id, status)
  WHERE status IN ('open', 'drafted');

SELECT app_apply_tenant_rls('pending_question');

COMMENT ON TABLE pending_question IS 'Dúvidas de clientes sem resposta (handoff), resolvidas quando o admin fornece a info.';
