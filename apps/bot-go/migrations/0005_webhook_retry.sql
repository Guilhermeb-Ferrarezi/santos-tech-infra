-- ============================================================================
-- Migration #5 — Retry interno de webhooks (F1 — expand)
-- ----------------------------------------------------------------------------
-- Expand/contract (doc 7.8): apenas adições — nenhuma coluna, índice ou
-- constraint existente é removido. ADD COLUMN IF NOT EXISTS / CREATE INDEX
-- IF NOT EXISTS são idempotentes (rerun seguro).
--
-- Contexto: a rota `/webhooks/:provider/:tenantId` dá ACK 200 SÍNCRONO à Meta
-- (exigência de < 20 s) — a Meta portanto NUNCA reentrega um evento que falhou
-- depois do ACK. A recuperação tem de ser INTERNA: o `WebhookRetryPoller`
-- (packages/api/src/webhook-retry.ts) redrena eventos 'failed' e reexecuta o
-- engine. Estas colunas dão suporte a esse ciclo de vida:
--   • processed_at  — quando o engine processou com sucesso (status 'done')
--   • last_error    — última falha (mensagem truncada ~500 chars, sem PII)
--   • attempts      — tentativas de retry já reivindicadas pelo poller
--   • next_retry_at — backoff exponencial (2^attempts min, teto 60 min)
--
-- Nenhum valor novo de enum: `webhook_processing_status` ('pending',
-- 'processing', 'done', 'failed' — ver 0001) já cobre o ciclo de vida.
-- RLS/grants de `webhook_events` ficam intocados: o poller roda como o DONO
-- (cross-tenant, mesmo padrão do OutboxRelay); a rota atualiza o status via
-- `withTenant()` (RLS ativa), usando o GRANT UPDATE já concedido em 0001.
-- ============================================================================

ALTER TABLE webhook_events
  -- Timestamp do processamento com sucesso (processing_status = 'done').
  ADD COLUMN IF NOT EXISTS processed_at  timestamptz,
  -- Última falha de processamento, truncada (~500 chars) — ids e mensagem de
  -- erro apenas; nunca conteúdo de mensagem ou telefone.
  ADD COLUMN IF NOT EXISTS last_error    text,
  -- Tentativas de retry já reivindicadas pelo poller (0 = nunca redrenado).
  ADD COLUMN IF NOT EXISTS attempts      integer NOT NULL DEFAULT 0,
  -- Próxima elegibilidade de retry (NULL = elegível imediatamente).
  ADD COLUMN IF NOT EXISTS next_retry_at timestamptz;

COMMENT ON COLUMN webhook_events.processed_at IS
  'Quando o engine processou o evento com sucesso (processing_status = done).';
COMMENT ON COLUMN webhook_events.last_error IS
  'Última falha de processamento (mensagem truncada ~500 chars; sem PII).';
COMMENT ON COLUMN webhook_events.attempts IS
  'Tentativas de retry reivindicadas pelo WebhookRetryPoller (dead-letter ao atingir maxAttempts).';
COMMENT ON COLUMN webhook_events.next_retry_at IS
  'Backoff exponencial do retry (2^attempts min, teto 60 min). NULL = elegível já.';

-- Índice do poller: eventos 'failed' vencidos (claim com FOR UPDATE SKIP
-- LOCKED). Parcial — o conjunto 'failed' é pequeno; 'pending'/'done' ficam fora.
CREATE INDEX IF NOT EXISTS idx_webhook_events_retry_due
  ON webhook_events (next_retry_at)
  WHERE processing_status = 'failed';
