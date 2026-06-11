-- ============================================================================
-- Migration #2 — Colunas para o follow-up scheduler (F2 — expand)
-- ----------------------------------------------------------------------------
-- Expand/contract (doc 7.8): apenas adições — nenhuma coluna, índice ou constraint
-- existente é removido. O step contract (drop de colunas obsoletas) ocorre em
-- migração futura, após todos os leitores estarem atualizados.
--
-- Contexto: Task 1.10 adiciona o poller `FollowUpScheduler` que opera sobre
-- `scheduled_contacts` para reativar leads com respeito às regras Meta (seção 2.6).
-- Os novos valores de enum e colunas são necessários para implementar:
--   • gate de tentativas (max_attempts + attempts)
--   • regra 1/dia por lead (last_attempted_at)
--   • rastreio de sucesso (sent_at)
--   • estados terminais distintos (exhausted, skipped)
-- ============================================================================

-- ----------------------------------------------------------------------------
-- Novos valores no enum scheduled_status
-- ADD VALUE IF NOT EXISTS é seguro para rerun (idempotente).
-- IMPORTANTE: ALTER TYPE ... ADD VALUE não é transacional no Postgres → commita
-- imediatamente mesmo dentro de BEGIN/COMMIT. Usar IF NOT EXISTS p/ reentrância.
-- ----------------------------------------------------------------------------
ALTER TYPE scheduled_status ADD VALUE IF NOT EXISTS 'exhausted';
ALTER TYPE scheduled_status ADD VALUE IF NOT EXISTS 'skipped';

-- ----------------------------------------------------------------------------
-- Novas colunas em scheduled_contacts
-- ADD COLUMN IF NOT EXISTS é idempotente (Postgres 9.6+).
-- ----------------------------------------------------------------------------
ALTER TABLE scheduled_contacts
  -- Máximo de tentativas de envio por registro. Doc 2.6: "MAX 2-3 tentativas,
  -- nunca > 3". O CHECK garante a invariante no nível de banco.
  ADD COLUMN IF NOT EXISTS max_attempts     INTEGER    NOT NULL DEFAULT 3
    CONSTRAINT sc_max_attempts_range CHECK (max_attempts BETWEEN 1 AND 3),
  -- Última tentativa de envio (sucesso ou falha). Usado pelo gate da regra 1/dia:
  -- o scheduler pula qualquer registro com last_attempted_at = hoje (UTC).
  ADD COLUMN IF NOT EXISTS last_attempted_at TIMESTAMPTZ,
  -- Timestamp do envio com sucesso. Preenchido quando status transita para 'fired'.
  ADD COLUMN IF NOT EXISTS sent_at          TIMESTAMPTZ;

COMMENT ON COLUMN scheduled_contacts.max_attempts
  IS 'Máximo de tentativas de envio (1-3; nunca > 3 — regras Meta, doc 2.6).';
COMMENT ON COLUMN scheduled_contacts.last_attempted_at
  IS 'Última tentativa de envio — gate da regra 1/dia (UTC) do follow-up scheduler.';
COMMENT ON COLUMN scheduled_contacts.sent_at
  IS 'Timestamp do envio com sucesso (status = fired).';
