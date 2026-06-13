-- migrate:no-transaction
-- ============================================================================
-- Migration #21 — novo valor 'evolution' no enum channel
-- ----------------------------------------------------------------------------
-- O número não-oficial (Evolution/Baileys) é um canal separado do 'whatsapp'
-- oficial (Meta Cloud API). ALTER TYPE ADD VALUE não roda em transação → diretiva
-- migrate:no-transaction na 1ª linha. IF NOT EXISTS torna idempotente.
-- ============================================================================
ALTER TYPE channel ADD VALUE IF NOT EXISTS 'evolution';
