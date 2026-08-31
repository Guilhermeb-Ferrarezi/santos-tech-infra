-- Índice do LATERAL de GET /api/leads.
--
-- A listagem faz, por lead, um LATERAL que busca a conversa mais recente do
-- contato (ORDER BY last_inbound_at DESC LIMIT 1). conversation NÃO tem
-- contact_id direto — o vínculo é indireto via channel_identity_id (um
-- contato pode ter mais de um canal/número) — a query resolve o(s)
-- channel_identity_id do contato via idx_channel_identity_contact (0001_init.sql)
-- e depois busca a conversa mais recente entre eles; este índice cobre essa
-- segunda parte, senão vira um scan de conversation por channel_identity_id.
CREATE INDEX IF NOT EXISTS idx_conversation_channel_identity_last_inbound
  ON conversation (tenant_id, channel_identity_id, last_inbound_at DESC);
