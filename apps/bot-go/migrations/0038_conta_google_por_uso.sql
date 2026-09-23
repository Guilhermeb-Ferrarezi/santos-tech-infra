-- Cada conta Google autoriza UM propósito, não todos.
--
-- O desenho anterior pedia Agenda e Drive na mesma tela, para qualquer conta.
-- Isso estava errado para como a escola trabalha:
--
--   mizakesgschool (Henrique)  -> agenda pessoal, e SÓ isso
--   ceo.santosgames (Rodrigo)  -> agenda pessoal, e SÓ isso
--   diretoria                  -> Drive, onde ficam os arquivos sensíveis
--                                 da empresa (plano de 5 TB)
--
-- Pedir Drive na conta do Henrique seria pedir acesso que ele não precisa dar;
-- pedir Agenda na conta da diretoria criaria evento onde ninguém olha. Cada
-- autorização passa a ser separada por propósito, e o bot só usa cada conta
-- para aquilo que ela autorizou.
--
-- NOTA SOBRE O NOME DA TABELA: continua google_calendar_account por ser a que
-- já existe em produção com dados. Renomear no meio do caminho é risco sem
-- retorno; o que importa é que agora ela guarda contas Google por propósito,
-- e não só agendas.

ALTER TABLE google_calendar_account
  ADD COLUMN IF NOT EXISTS usa_agenda boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS usa_drive  boolean NOT NULL DEFAULT false;

-- As contas que já existem foram autorizadas só para o Google Agenda. Os
-- defaults acima já descrevem exatamente isso, então NÃO é preciso ninguém
-- reautorizar o Henrique nem o Rodrigo: eles continuam recebendo os eventos,
-- e nenhum deles ganha acesso ao Drive por acidente.

-- Quem recebe evento de aula.
CREATE INDEX IF NOT EXISTS google_account_agenda_idx
  ON google_calendar_account (tenant_id)
  WHERE active AND usa_agenda;

-- Quem guarda os dossiês.
CREATE INDEX IF NOT EXISTS google_account_drive_idx
  ON google_calendar_account (tenant_id)
  WHERE active AND usa_drive;
