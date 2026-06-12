-- ============================================================================
-- Migration #16 — múltiplos administradores
-- ----------------------------------------------------------------------------
-- Substitui o número único (admin_whatsapp_number, 0014) por uma lista. A coluna
-- antiga é mantida por compatibilidade, mas a lista passa a ser a fonte de verdade.
-- ============================================================================
ALTER TABLE tenant_config
  ADD COLUMN IF NOT EXISTS admin_whatsapp_numbers jsonb NOT NULL DEFAULT '[]'::jsonb;

-- Migra o número único existente (se houver) para a lista.
UPDATE tenant_config
SET admin_whatsapp_numbers = jsonb_build_array(admin_whatsapp_number)
WHERE admin_whatsapp_number <> ''
  AND (admin_whatsapp_numbers IS NULL OR admin_whatsapp_numbers = '[]'::jsonb);
