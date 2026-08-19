-- Índice do LATERAL de GET /api/leads.
--
-- A listagem faz, por lead, um LATERAL que busca a conversa mais recente do
-- contato (ORDER BY last_inbound_at DESC LIMIT 1). Sem este índice cada linha
-- vira um scan de conversation filtrado por (tenant_id, contact_id).
--
-- O outro LATERAL (external_id) já é coberto por idx_channel_identity_contact,
-- criado em 0001_init.sql.
CREATE INDEX IF NOT EXISTS idx_conversation_contact_last_inbound
  ON conversation (tenant_id, contact_id, last_inbound_at DESC);
