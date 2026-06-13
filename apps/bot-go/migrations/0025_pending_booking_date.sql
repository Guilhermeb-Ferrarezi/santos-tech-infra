-- ============================================================================
-- Migration #25 — data exata (ISO) do agendamento na pending_booking
-- ----------------------------------------------------------------------------
-- proposed_day guardava só o rótulo humano ("quinta"), e o resolvedor caía na
-- próxima ocorrência do dia da semana — gravando a aula no dia errado (ex.: cliente
-- pede "30 de julho", grava na próxima quinta). Agora o LLM informa a data exata
-- (YYYY-MM-DD) e a gente grava direto, sem adivinhar.
--
-- Idempotente. Default '' para linhas antigas.
-- ============================================================================

ALTER TABLE pending_booking
  ADD COLUMN IF NOT EXISTS proposed_date text NOT NULL DEFAULT '';

COMMENT ON COLUMN pending_booking.proposed_date IS
  'Data exata ISO YYYY-MM-DD proposta (calculada pelo LLM). Preferida sobre proposed_day na hora de gravar no Notion.';
