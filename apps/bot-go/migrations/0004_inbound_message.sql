-- ============================================================================
-- Migration #4 — Mensagens inbound canônicas (memória de conversa — expand)
-- ----------------------------------------------------------------------------
-- Expand/contract (doc 7.8): apenas adições — nenhuma coluna, índice ou
-- constraint existente é removido. Operações idempotentes (IF NOT EXISTS /
-- guarda de política) para rerun seguro.
--
-- Contexto: até aqui só as mensagens do BOT eram persistidas como turnos
-- canônicos (`outbound_message`); o inbound do cliente vivia cru em
-- `webhook_events`, sem vínculo com a conversa. Consequências: o histórico do
-- prompt continha só turnos `assistant` (o LLM ficava sem memória) e o painel
-- admin só listava mensagens do bot. Esta tabela fecha a lacuna: cada mensagem
-- do cliente vira um turno canônico ligado à `conversation`, e o histórico
-- passa a ser o UNION cronológico de `inbound_message` + `outbound_message`.
-- ============================================================================

-- ----------------------------------------------------------------------------
-- inbound_message — turno canônico do CLIENTE (espelho de outbound_message).
-- Gravada pelo `PgMessageRecorder` (port `MessageRecorder` de @bot/core).
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS inbound_message (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  conversation_id      uuid NOT NULL,
  provider_message_id  text NOT NULL,
  content              jsonb NOT NULL,
  received_at          timestamptz NOT NULL,
  created_at           timestamptz NOT NULL DEFAULT now(),
  -- Idempotência de gravação: retry de webhook com o mesmo id do provider não
  -- duplica o turno. ESCOPADA A TENANT (convenção 7.8 + mesmo racional da dedup
  -- de webhook_events: unicidade global vazaria existência cross-tenant).
  UNIQUE (tenant_id, provider_message_id),
  -- FK composta garante que a conversa é do MESMO tenant (isolamento no DB).
  FOREIGN KEY (tenant_id, conversation_id)
    REFERENCES conversation (tenant_id, id) ON DELETE CASCADE
);

-- Leitura do histórico: últimos N turnos da conversa, mais recentes primeiro.
CREATE INDEX IF NOT EXISTS idx_inbound_message_conv
  ON inbound_message (tenant_id, conversation_id, received_at DESC);

-- RLS + FORCE + política tenant_isolation + GRANT DML a bot_app (acoplados) —
-- mesma rotina de TODAS as tabelas escopadas a tenant (ver 0001). A guarda DO
-- existe só para idempotência: o CREATE POLICY dentro do helper não tem
-- IF NOT EXISTS.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT FROM pg_policies
    WHERE schemaname = 'public'
      AND tablename  = 'inbound_message'
      AND policyname = 'tenant_isolation'
  ) THEN
    PERFORM app_apply_tenant_rls('inbound_message');
  END IF;
END
$$;

COMMENT ON TABLE inbound_message IS
  'Turno canônico do CLIENTE. Dedup (tenant_id, provider_message_id); histórico do prompt/painel = UNION cronológico com outbound_message.';

-- Revisão de segurança: a app nunca atualiza `tenants` legitimamente (0001
-- concedeu SELECT, UPDATE; CRUD de tenant é do dono) → revoga o UPDATE.
REVOKE UPDATE ON tenants FROM bot_app;
