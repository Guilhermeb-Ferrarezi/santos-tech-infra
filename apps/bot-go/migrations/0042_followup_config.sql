-- Follow-up e reativação — fase 2 (spec 2026-09-25-bot-follow-up-reativacao).
--
-- Três escolhas que antes estavam no código e passam a ser da escola, na tela
-- WhatsApp · Configurações (princípio do Henrique: todo parâmetro tem controle
-- na interface).

-- O que o bot faz quando chega o dia de um retorno:
--   avisar             — só avisa um humano (é o que a fase 1 já fazia)
--   reativar           — o bot manda a mensagem ao cliente sozinho
--   reativar_e_avisar  — manda e avisa
-- "avisar" é o padrão de propósito: mensagem automática errada queima o lead, e
-- o modo automático tem que ser uma escolha explícita de alguém.
ALTER TABLE tenant_config
  ADD COLUMN IF NOT EXISTS followup_modo text NOT NULL DEFAULT 'avisar';

-- Quantos dias depois da aula experimental o retorno dispara (9h de Brasília).
-- 0 desliga o retorno pós-experimental.
ALTER TABLE tenant_config
  ADD COLUMN IF NOT EXISTS followup_dias_pos_experimental int NOT NULL DEFAULT 2;

-- Conta da plataforma que recebe a Tarefa do retorno. NULL = a do ambiente
-- (FOLLOWUP_RESPONSAVEL_ID), que é como a fase 1 funcionava.
ALTER TABLE tenant_config
  ADD COLUMN IF NOT EXISTS followup_responsavel_id int;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'tenant_config_followup_modo_valido') THEN
    ALTER TABLE tenant_config ADD CONSTRAINT tenant_config_followup_modo_valido
      CHECK (followup_modo IN ('avisar', 'reativar', 'reativar_e_avisar'));
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'tenant_config_followup_dias_valido') THEN
    ALTER TABLE tenant_config ADD CONSTRAINT tenant_config_followup_dias_valido
      CHECK (followup_dias_pos_experimental BETWEEN 0 AND 30);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'tenant_config_followup_responsavel_valido') THEN
    ALTER TABLE tenant_config ADD CONSTRAINT tenant_config_followup_responsavel_valido
      CHECK (followup_responsavel_id IS NULL OR followup_responsavel_id > 0);
  END IF;
END $$;

-- Um retorno pós-experimental por aula, nunca dois. O worker enfileira a aula
-- no dia do retorno; este índice é o que impede duas réplicas (ou dois ciclos)
-- de enfileirá-la de novo — inclusive depois de disparada ou cancelada, porque
-- não filtra por status.
CREATE UNIQUE INDEX IF NOT EXISTS uq_scheduled_pos_experimental
  ON scheduled_contacts (tenant_id, (payload->>'notionPageId'))
  WHERE payload->>'kind' = 'pos_experimental';
