-- ============================================================================
-- Migration #26 — tipo do registro no log de processamento
-- ----------------------------------------------------------------------------
-- Até aqui todo registro era um turno de conversa (mensagem recebida → resposta).
-- Agora o log também guarda AÇÕES do sistema que não vêm de uma mensagem (ex.:
-- agendamento gravado no Notion), com kind='action'.
--
-- Idempotente. Linhas antigas assumem 'message'.
-- ============================================================================

ALTER TABLE bot_processing_log
  ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'message';

COMMENT ON COLUMN bot_processing_log.kind IS
  'Tipo do registro: message (turno de conversa) | action (ação do sistema, ex.: agendamento no Notion).';
