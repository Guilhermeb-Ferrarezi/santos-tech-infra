-- ============================================================================
-- Migration #19 — normaliza o status do lead para o funil do CRM
-- ----------------------------------------------------------------------------
-- Funil: novo → em_atendimento → aula_marcada → matriculado → perdido.
-- Leads antigos ('new'/'open'/vazio) viram 'novo'. Coluna lead.status já existe (0001).
-- ============================================================================
UPDATE lead
SET status = 'novo'
WHERE status IS NULL OR status IN ('new', 'open', '');

-- Índice para listar o funil por tenant + status.
CREATE INDEX IF NOT EXISTS idx_lead_tenant_status ON lead (tenant_id, status);
