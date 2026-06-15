-- ============================================================================
-- Migration #27 — toggle "captar leads do Evolution" (on por padrão)
-- ----------------------------------------------------------------------------
-- Independente do evolution_bot_reply_enabled (que controla a RESPOSTA do bot).
-- Quando desligado, mensagens recebidas pela Evolution são ignoradas por completo:
-- não viram lead no CRM e o bot não responde.
-- Idempotente. Default true para não mudar o comportamento atual ao migrar.
-- ============================================================================
ALTER TABLE tenant_config
  ADD COLUMN IF NOT EXISTS evolution_lead_capture_enabled boolean NOT NULL DEFAULT true;
