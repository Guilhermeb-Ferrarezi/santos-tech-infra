-- Qualificação do lead — o que a escola sabe sobre cada pessoa.
--
-- POR QUE POR CONTATO, NÃO POR CONVERSA. A mesma família volta semanas depois
-- por outro assunto, às vezes numa conversa nova. O que se descobriu sobre ela
-- — que o filho tem 14 anos, que o interesse é programação, que a motivação é
-- o mercado de trabalho — não pode morrer junto com a thread. É memória da
-- PESSOA, e conversa é só onde ela apareceu.
--
-- POR QUE COLUNAS E NÃO UM JSONB SOLTO. As perguntas são fixas e conhecidas;
-- colunas deixam a coordenação filtrar ("quem quer programação e está livre de
-- manhã") sem escrever SQL de JSON. O que não cabe nelas vai em observacoes.
--
-- A tabela conversation já tinha structured_facts jsonb, lido pelo prompt e
-- NUNCA escrito por ninguém — memória que existia só no papel. Esta tabela é o
-- que aquilo deveria ter sido.

CREATE TABLE IF NOT EXISTS lead_qualificacao (
  tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  contact_id uuid NOT NULL REFERENCES contact (id) ON DELETE CASCADE,

  -- O que o cliente conta.
  para_quem       text NOT NULL DEFAULT '',  -- 'proprio' | 'filho' | 'outro'
  aluno_nome      text NOT NULL DEFAULT '',
  aluno_idade     int  NOT NULL DEFAULT 0,   -- 0 = adulto ou não informado
  interesse       text NOT NULL DEFAULT '',  -- programação, modelagem 3D, Excel...
  ja_faz_curso    text NOT NULL DEFAULT '',  -- 'sim' | 'nao' | '' (não perguntado)
  disponibilidade text NOT NULL DEFAULT '',  -- "manhãs de quinta", "só sábado"
  motivacao       text NOT NULL DEFAULT '',  -- nas palavras do cliente
  motivacao_tipo  text NOT NULL DEFAULT '',  -- ver qualificacao.go
  observacoes     text NOT NULL DEFAULT '',

  -- O que ACONTECEU. Escrito pelo código a partir de fatos, nunca pelo modelo:
  -- é daqui que sai o grau, e um grau que o modelo inventa não classifica nada.
  preco_informado       boolean NOT NULL DEFAULT false,
  aula_marcada          boolean NOT NULL DEFAULT false,
  perguntas_respondidas int     NOT NULL DEFAULT 0,

  -- TURNOS, não campos: quem despeja cinco fatos numa frase conversou UMA vez.
  -- É a diferença entre o lead que troca ideia e o que só quer o número.
  turnos_respondendo    int     NOT NULL DEFAULT 0,
  -- A válvula de escape precisa de memória: "na segunda vez, informe o valor"
  -- não existe se ninguém contar as vezes.
  pedidos_de_preco      int     NOT NULL DEFAULT 0,

  criado_em     timestamptz NOT NULL DEFAULT now(),
  atualizado_em timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (tenant_id, contact_id)
);

-- A coordenação abre a lista por quem foi mexido mais recentemente.
CREATE INDEX IF NOT EXISTS idx_lead_qualificacao_recentes
  ON lead_qualificacao (tenant_id, atualizado_em DESC);
