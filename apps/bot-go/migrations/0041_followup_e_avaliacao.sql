-- O que aconteceu DEPOIS da aula experimental.
--
-- Hoje o sistema sabe marcar a aula e esquece dela no minuto seguinte. Ninguém
-- registra se a pessoa apareceu, e por isso ninguém sabe a única coisa que
-- importa: quantos dos leads que o bot qualificou viram aluno. Sem esta tabela,
-- "o bot está funcionando" é opinião.
--
-- UMA LINHA POR AULA, chaveada pelo `notion_page_id` — que é como a aula é
-- identificada em todo o resto do sistema (booking_reminder, cancelamento,
-- Google Agenda). Não por conversa: a mesma família marca duas aulas para dois
-- filhos, e são dois resultados diferentes.
--
-- POR QUE NÃO NASCE JUNTO COM A AULA. A linha só aparece quando alguém marca o
-- resultado. A lista do painel sai de `booking_reminder` (que já é o livro-razão
-- das aulas do bot) com LEFT JOIN aqui — assim uma aula marcada agora aparece na
-- lista imediatamente, sem job de sincronia para dar errado, e "ainda não
-- marcado" é a ausência da linha em vez de mais um estado para confundir.
--
-- LIMITE ASSUMIDO: só entram aulas que o BOT marcou. Aula lançada à mão no
-- Notion pela coordenação não tem telefone nem data-hora em coluna nenhuma (a
-- base da escola guarda dia e horário como texto), então não há como casá-la com
-- um cliente. Quem quiser o funil completo precisa marcar pelo bot.

CREATE TABLE IF NOT EXISTS aula_resultado (
  tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  notion_page_id text NOT NULL,

  -- Copiados da aula no momento em que o resultado é marcado. Redundantes de
  -- propósito: a aula pode ser arquivada no Notion e os lembretes apagados, e o
  -- resultado precisa continuar legível anos depois.
  client_phone text NOT NULL DEFAULT '',
  aluno        text NOT NULL DEFAULT '',
  aula_em      timestamptz,

  -- '' nunca é gravado aqui: linha sem resultado não existe (ver acima).
  --   faltou      — não apareceu
  --   veio        — apareceu, ainda decidindo
  --   fechou      — apareceu e matriculou
  --   nao_fechou  — apareceu e não quis
  resultado text NOT NULL,

  observacao  text NOT NULL DEFAULT '',
  marcado_em  timestamptz NOT NULL DEFAULT now(),
  marcado_por text NOT NULL DEFAULT '',

  PRIMARY KEY (tenant_id, notion_page_id),
  CONSTRAINT aula_resultado_valido
    CHECK (resultado IN ('faltou', 'veio', 'fechou', 'nao_fechou'))
);

-- O relatório que motiva a tabela: conversão por período.
CREATE INDEX IF NOT EXISTS idx_aula_resultado_por_aula
  ON aula_resultado (tenant_id, aula_em DESC);

-- Quem já foi convidado a avaliar no Google.
--
-- POR PESSOA, não por aula: pedir de novo a quem já avaliou é o erro que esta
-- tabela existe para impedir. A mesma família com dois filhos avalia uma vez.
--
-- ⚠️ `avaliou` É UMA ANOTAÇÃO DA ESCOLA, NÃO UM FATO VERIFICADO. O Google não
-- diz quem avaliou, e a avaliação pode estar com outro nome. Quem marca aqui é
-- uma pessoa que conferiu o perfil ou ouviu do cliente. Tratar esta coluna como
-- verdade auditável levaria a conclusões erradas sobre a taxa de resposta.
--
-- ⚠️ E não filtre o convite por quem gostou. Pedir avaliação só a quem elogiou é
-- "review gating", proibido pelas políticas do Google e passível de remoção do
-- perfil. O convite vai para todo mundo que teve aula de verdade; o que esta
-- tabela controla é não pedir DUAS vezes.
CREATE TABLE IF NOT EXISTS avaliacao_google (
  tenant_id    uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  client_phone text NOT NULL,
  aluno        text NOT NULL DEFAULT '',

  --   nao_pedido — ainda não foi convidado
  --   pedido     — convidado, sem resposta
  --   avaliou    — a escola entende que avaliou (ver ressalva acima)
  --   recusou    — disse que não quer avaliar
  --   nao_pedir  — a escola decidiu não convidar esta pessoa
  status text NOT NULL DEFAULT 'nao_pedido',

  pedido_em     timestamptz,
  lembrado_em   timestamptz,
  respondido_em timestamptz,
  observacao    text NOT NULL DEFAULT '',
  atualizado_em timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (tenant_id, client_phone),
  CONSTRAINT avaliacao_google_status_valido
    CHECK (status IN ('nao_pedido', 'pedido', 'avaliou', 'recusou', 'nao_pedir'))
);

-- "Quem já teve aula e ainda não foi convidado" — a fila de trabalho da
-- coordenação, que é a consulta que o painel faz toda vez que abre.
CREATE INDEX IF NOT EXISTS idx_avaliacao_google_status
  ON avaliacao_google (tenant_id, status);
