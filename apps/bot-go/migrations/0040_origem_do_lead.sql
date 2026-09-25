-- De onde o lead veio.
--
-- A escola paga anúncio, mantém blog, aparece no Google e tem um perfil no
-- Instagram, e hoje não sabe qual desses traz matrícula. Sem esta coluna,
-- "investir mais em anúncio" e "escrever mais no blog" são a mesma aposta no
-- escuro.
--
-- POR QUE FICA NO DOSSIÊ DA PESSOA, e não na conversa: a origem é como a pessoa
-- CHEGOU, e chega-se uma vez só. A mesma família volta semanas depois numa
-- conversa nova — e continua tendo vindo do mesmo anúncio.
--
-- ⚠️ NÃO CONFUNDIR COM `lead.origin` (migration 0020). Aquela coluna diz por
-- qual NÚMERO a pessoa falou — 'oficial' (Meta Cloud API) ou 'evolution' —, que
-- é infraestrutura, não marketing. Esta aqui diz o que fez a pessoa escrever.
-- São perguntas diferentes e as duas continuam valendo.
--
-- `origem` é vocabulário fechado (ver origem.go). Texto livre viraria dez
-- grafias da mesma coisa e não somaria em relatório nenhum. `origem_detalhe`
-- é a linha que a coordenação lê: QUAL anúncio, com título e id.
--
-- `origem_fonte` é quem afirmou, e existe porque as três procedências não valem
-- o mesmo. 'anuncio' é fato que a Meta mandou; 'marcador' é o texto pronto do
-- link, que o cliente pode ter editado; 'perguntado' é o cliente lembrando de
-- memória — o mais fraco dos três, e o único que pode estar simplesmente errado.
-- Sem esta coluna, um relatório somaria certeza com palpite.

ALTER TABLE lead_qualificacao
  ADD COLUMN IF NOT EXISTS origem         text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS origem_detalhe text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS origem_fonte   text NOT NULL DEFAULT '';

-- O relatório que motiva tudo isto: quantos leads por origem.
CREATE INDEX IF NOT EXISTS idx_lead_qualificacao_origem
  ON lead_qualificacao (tenant_id, origem);

-- Os textos prontos dos links `wa.me`, um por lugar onde a escola publica.
--
-- Fica na config do tenant, e não no código, porque quem muda o texto de um
-- link é a escola, numa tarde, sem esperar deploy. Formato:
--   [{"marcador":"vim pelo site da escola","origem":"site"}, ...]
ALTER TABLE tenant_config
  ADD COLUMN IF NOT EXISTS origem_marcadores jsonb NOT NULL DEFAULT '[]'::jsonb;
