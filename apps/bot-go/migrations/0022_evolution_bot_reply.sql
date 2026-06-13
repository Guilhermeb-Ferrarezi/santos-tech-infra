-- ============================================================================
-- Migration #22 — toggle "bot responde" no número da Evolution (off por padrão)
-- ============================================================================
ALTER TABLE tenant_config
  ADD COLUMN IF NOT EXISTS evolution_bot_reply_enabled boolean NOT NULL DEFAULT false;
