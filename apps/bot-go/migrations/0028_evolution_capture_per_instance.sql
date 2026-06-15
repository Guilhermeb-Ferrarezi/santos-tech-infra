-- ============================================================================
-- Migration #28 — captação de leads do Evolution POR número (instância)
-- ----------------------------------------------------------------------------
-- Antes a captação era um toggle global (evolution_lead_capture_enabled). Agora
-- cada número/instância pode ser ligado/desligado individualmente. Guardamos a
-- LISTA DE INSTÂNCIAS DESLIGADAS (nomes da Evolution); ausência = captando.
-- Default '[]' (todas captando) preserva o comportamento atual.
-- Idempotente.
-- ============================================================================
ALTER TABLE tenant_config
  ADD COLUMN IF NOT EXISTS evolution_capture_disabled jsonb NOT NULL DEFAULT '[]'::jsonb;
