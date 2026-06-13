-- ============================================================================
-- Migration #24 — nome do ALUNO (informado na conversa) na pending_booking
-- ----------------------------------------------------------------------------
-- Antes só guardávamos client_name (o nome do perfil do WhatsApp, ex.: "moto da
-- apple"). O nome que o cliente DIZ na conversa ("Guilherme") vinha no
-- schedulingRequest.studentName mas era descartado. Agora persiste, pra gravar no
-- Notion "Guilherme (moto da apple)" em vez de só o nome do WhatsApp.
--
-- Idempotente. Default '' para linhas antigas.
-- ============================================================================

ALTER TABLE pending_booking
  ADD COLUMN IF NOT EXISTS student_name text NOT NULL DEFAULT '';

COMMENT ON COLUMN pending_booking.student_name IS
  'Nome do aluno informado na conversa (schedulingRequest.studentName). client_name é o nome do perfil do WhatsApp.';
