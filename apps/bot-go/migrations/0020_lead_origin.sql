-- ============================================================================
-- Migration #20 — origem do lead (oficial vs evolution)
-- ----------------------------------------------------------------------------
-- Distingue de qual canal o lead veio: 'oficial' (bot Cloud API) ou 'evolution'
-- (número não-oficial via Evolution/Baileys). Default 'oficial' para os existentes.
-- ============================================================================
ALTER TABLE lead ADD COLUMN IF NOT EXISTS origin text NOT NULL DEFAULT 'oficial';
