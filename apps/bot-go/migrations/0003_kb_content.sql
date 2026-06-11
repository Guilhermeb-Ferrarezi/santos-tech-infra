-- ============================================================================
-- Migration #3 — Base de Conhecimento Tier1 em tenant_config (F1 — expand)
-- ----------------------------------------------------------------------------
-- Expand/contract (doc 7.8): apenas adição de coluna — nada existente é alterado
-- ou removido. ADD COLUMN IF NOT EXISTS é idempotente (Postgres 9.6+).
--
-- Contexto (Task 1.12): o `ClaudeResponder` (Task 1.9) injeta a KB inteira no
-- system prompt (Tier1, sem RAG no F1 — doc 2.8). A fonte da KB é o port local
-- `KnowledgeBaseProvider`; sua implementação SQL (`PgKnowledgeBaseProvider`) lê
-- esta coluna.
--
-- Por que em `tenant_config` (coluna JSONB) e não numa tabela nova:
--   • doc 2.8 — F1 usa SÓ Tier1 (KB inteira no prompt). Não há busca/filtro por
--     entrada, então uma tabela normalizada + índices seria over-engineering
--     (doc 12: "KB = Tier1 no prompt no F1. Sem pgvector/embeddings/full-text").
--   • A regra de ouro do schema (doc 2.4) proíbe colunas novas em
--     `contact`/`conversation` — NÃO em `tenant_config`, que é justamente a
--     "config hot do tenant" e já mistura colunas estritas + JSONB (doc 11).
--   • Tier2/Tier3 (full-text / pgvector) entram como TABELA nova quando a escala
--     pedir, FK'ando o tenant — sem tocar nesta coluna (futuro aditivo).
--
-- Formato: array JSON de entradas `{ id, title?, content }` (espelha
-- `KnowledgeBaseEntry` em @bot/adapters). Default '[]' → tenant sem KB responde
-- sem Tier1 (o gap-detector então sinaliza kb.gap_detected, doc 2.8).
-- ============================================================================

ALTER TABLE tenant_config
  ADD COLUMN IF NOT EXISTS kb_content jsonb NOT NULL DEFAULT '[]'::jsonb;

COMMENT ON COLUMN tenant_config.kb_content IS
  'KB Tier1 (doc 2.8): array JSON [{id,title?,content}] injetado inteiro no prompt. Tier2/3 = tabela nova futura.';
