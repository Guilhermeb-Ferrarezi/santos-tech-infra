-- ============================================================================
-- Migration #6 — Estilo de comunicação do contato (espelhamento — expand)
-- ----------------------------------------------------------------------------
-- Expand/contract (doc 7.8): apenas adições — nenhuma coluna, índice ou
-- constraint existente é removido. ADD COLUMN IF NOT EXISTS é idempotente
-- (rerun seguro).
--
-- Contexto (doc 2.2 — espelhamento sticky): o bot espelha o ESTILO do cliente
-- (formalidade, emojis, brevidade). O estilo é classificado UMA vez a partir
-- das primeiras mensagens inbound (histerese simplificada do F1: classifica e
-- congela; reclassificação contínua com histerese real é F2) e persistido como
-- estado do contato — nunca reavaliado a cada turno. O classificador roda no
-- worker (assinante de `message.received`) com um modelo barato (Haiku).
--
-- A coluna é TEXT deliberadamente — SEM enum Postgres: a fonte da verdade dos
-- valores é o tipo de domínio `CommunicationStyle` (Zod, @bot/shared
-- conversation.ts). Valores hoje: 'formal' | 'technical' | 'casual' | 'plain'.
-- Valor novo = mudança só no Zod, sem ALTER TYPE (mesmo racional do
-- `domain_events.event_type` em 0001). NULL = ainda não classificado.
--
-- RLS/grants de `contact` ficam intocados: app_apply_tenant_rls('contact')
-- (0001) já cobre a tabela inteira, colunas novas inclusive.
-- ============================================================================

ALTER TABLE contact
  -- Estilo de comunicação detectado (espelhamento sticky — doc 2.2).
  ADD COLUMN IF NOT EXISTS communication_style text;

COMMENT ON COLUMN contact.communication_style IS
  'Estilo de comunicação do cliente (espelhamento sticky, doc 2.2). Valores do Zod CommunicationStyle (@bot/shared): formal | technical | casual | plain. NULL = não classificado. Classificado UMA vez (histerese simplificada F1) pelo style-classifier do worker.';
