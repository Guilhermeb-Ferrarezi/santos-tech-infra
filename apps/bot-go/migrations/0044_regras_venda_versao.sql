-- Regras de venda editáveis na tela "Como o bot vende"
-- (spec 2026-09-25-bot-regras-venda-editaveis, no repo dashboard).
--
-- SÓ INSERÇÃO. Cada "Salvar" grava uma versão nova; a atual é a de maior seq.
-- Texto que muda o bot em produção precisa de desfazer: restaurar uma versão
-- antiga grava uma cópia dela como versão nova, e nada é apagado.
--
-- SEM LINHA = TUDO PADRÃO. O padrão mora no código (regras_venda.go). O
-- documento guarda só o que difere dele (RegrasVenda.SemPadrao), para que uma
-- melhoria do padrão chegue a quem nunca personalizou.
--
-- Fora de tenant_config de propósito: a tela Configurações salva o objeto de
-- config inteiro, e as regras não podem ser sobrescritas por ela.
--
-- Sem RLS, como as tabelas desde a 0034: o acesso filtra tenant_id na query.

CREATE TABLE IF NOT EXISTS bot_regras_venda_versao (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  -- Ordem das versões. Não se confia em salvo_em para isso: dois saves no
  -- mesmo milissegundo empatariam.
  seq           bigint GENERATED ALWAYS AS IDENTITY,
  tenant_id     uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  conteudo      jsonb NOT NULL,          -- RegrasVenda sem o que é igual ao padrão
  -- Resumo (hash) do texto padrão de cada parte na hora do save. Se o padrão
  -- do código mudar depois, a tela avisa quem tinha personalizado a parte.
  padrao_hash   jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- O que mudou em relação à versão anterior ("faixas", "vocabulario", nomes
  -- das partes), para a lista do histórico não precisar comparar documentos.
  alteracoes    text[] NOT NULL DEFAULT '{}',
  salvo_por     text NOT NULL,
  salvo_em      timestamptz NOT NULL DEFAULT now(),
  restaurada_de uuid REFERENCES bot_regras_venda_versao (id)
);

CREATE INDEX IF NOT EXISTS idx_regras_venda_versao_atual
  ON bot_regras_venda_versao (tenant_id, seq DESC);

COMMENT ON TABLE bot_regras_venda_versao IS
  'Versões das regras de venda editadas na tela. Só inserção; a atual é a de maior seq. Sem linha = padrão do código.';
