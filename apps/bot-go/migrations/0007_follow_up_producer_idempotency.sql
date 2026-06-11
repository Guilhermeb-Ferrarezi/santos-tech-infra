-- ============================================================================
-- Migration #7 — Idempotência do producer de follow-up (F2 — expand)
-- ----------------------------------------------------------------------------
-- Expand/contract (doc 7.8): apenas adições — nenhuma coluna, índice ou
-- constraint existente é removido. CREATE INDEX IF NOT EXISTS é idempotente
-- (rerun seguro).
--
-- Contexto: o `FollowUpProducer` (packages/worker/src/follow-up-producer.ts)
-- agenda follow-ups em `scheduled_contacts` reagindo a eventos do outbox
-- (`conversation.state_changed` → AWAITING_REPLY). A entrega do relay é
-- AT-LEAST-ONCE: o mesmo evento pode ser reentregue, e múltiplas instâncias
-- do worker podem processar eventos distintos da MESMA conversa em paralelo.
--
-- Este índice único PARCIAL torna o agendamento naturalmente idempotente:
-- no máximo UM registro ATIVO ('pending' ou 'processing') por conversa.
-- O producer insere com `ON CONFLICT (tenant_id, conversation_id) WHERE
-- status IN ('pending','processing') DO NOTHING` — reentrega/corrida viram
-- no-op, sem precisar do ledger `processed_events`.
--
-- Registros terminais (fired/cancelled/failed/exhausted/skipped) ficam FORA
-- do predicado: uma nova cadência para a mesma conversa (novo ciclo de
-- AWAITING_REPLY) cria um novo registro normalmente. Linhas com
-- conversation_id NULL (agendamentos sem conversa) não participam do índice
-- (NULLs não conflitam).
-- ============================================================================

-- O `kind` (payload->>'kind': 'follow_up' | 'reactivation') entra na CHAVE:
-- um follow-up pendente NÃO bloqueia uma reativação agendada para a mesma
-- conversa (e vice-versa) — são compromissos independentes (doc 2.5 × 2.6).
CREATE UNIQUE INDEX IF NOT EXISTS ux_scheduled_contacts_active_follow_up
  ON scheduled_contacts (tenant_id, conversation_id, (COALESCE(payload->>'kind', 'follow_up')))
  WHERE status IN ('pending', 'processing');

COMMENT ON INDEX ux_scheduled_contacts_active_follow_up IS
  'No máx. 1 scheduled_contact ATIVO (pending/processing) por conversa E POR KIND (follow_up/reactivation) — idempotência do producer (entrega at-least-once do outbox).';
