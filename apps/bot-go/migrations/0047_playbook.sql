-- Playbook de raciocínio de venda (spec 2026-09-25-bot-playbook-venda, no
-- repo dashboard). Fichas de situação que o bot consulta quando reconhece o
-- caso: sinal → o que costuma estar por trás → como conduzir → o que evitar.
--
-- NADA ENTRA NO BOT SEM UM HUMANO ATIVAR: ficha nasce 'rascunho'. Arquivar não
-- apaga — a medição de uso continua apontando para ela.
--
-- Sem RLS, como as tabelas desde a 0034: o acesso filtra tenant_id na query.

CREATE TABLE IF NOT EXISTS bot_playbook_situacao (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  titulo       text NOT NULL,
  sinais       text NOT NULL DEFAULT '',   -- quando reconhecer
  por_tras     text NOT NULL DEFAULT '',   -- o que costuma estar por trás
  conduzir     text NOT NULL DEFAULT '',   -- como conduzir
  evitar       text NOT NULL DEFAULT '',   -- o que evitar (opcional)
  -- Para quem vale. Vazio = qualquer. motivos são valores de motivacaoTipo
  -- (qualificacao.go); para_quem é 'proprio' | 'filho' | 'outro'.
  motivos      text[] NOT NULL DEFAULT '{}',
  para_quem    text NOT NULL DEFAULT '',
  caso_real    text NOT NULL DEFAULT '',   -- de onde veio o aprendizado
  conversa_id  uuid,                       -- conversa de origem, se houver
  estado       text NOT NULL DEFAULT 'rascunho',
  origem       text NOT NULL DEFAULT 'manual',
  criado_por   text NOT NULL,
  criado_em    timestamptz NOT NULL DEFAULT now(),
  alterado_por text NOT NULL,
  alterado_em  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT bot_playbook_estado_valido CHECK (estado IN ('rascunho', 'ativa', 'arquivada')),
  CONSTRAINT bot_playbook_origem_valida CHECK (origem IN ('manual', 'ia', 'whatsapp')),
  CONSTRAINT bot_playbook_para_quem_valido CHECK (para_quem IN ('', 'proprio', 'filho', 'outro'))
);

-- A consulta quente: as ativas do tenant, a cada mensagem.
CREATE INDEX IF NOT EXISTS idx_playbook_situacao_ativas
  ON bot_playbook_situacao (tenant_id, alterado_em DESC)
  WHERE estado = 'ativa';

-- Em que conversa o bot disse que usou cada ficha. É a base do "usada em N
-- conversas → M marcaram → K fecharam". Só ids que estavam no prompt entram
-- (o engine descarta o resto).
CREATE TABLE IF NOT EXISTS bot_playbook_uso (
  tenant_id       uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  situacao_id     uuid NOT NULL REFERENCES bot_playbook_situacao (id),
  contact_id      uuid NOT NULL,
  conversation_id uuid NOT NULL,
  usado_em        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_playbook_uso_por_situacao
  ON bot_playbook_uso (tenant_id, situacao_id, usado_em DESC);

-- Linha do tempo das mudanças (criou/editou/ativou/arquivou), para os
-- indicadores marcarem "26/09 — ativada 'Gostou, mas vai espaçar'".
CREATE TABLE IF NOT EXISTS bot_playbook_evento (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  situacao_id uuid NOT NULL REFERENCES bot_playbook_situacao (id),
  acao        text NOT NULL,
  por         text NOT NULL,
  em          timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT bot_playbook_evento_acao_valida CHECK (acao IN ('criou', 'editou', 'ativou', 'arquivou', 'voltou_rascunho'))
);
CREATE INDEX IF NOT EXISTS idx_playbook_evento_tempo
  ON bot_playbook_evento (tenant_id, em DESC);
