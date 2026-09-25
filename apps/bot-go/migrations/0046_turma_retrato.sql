-- Memória própria do bot sobre as turmas (spec 2026-09-25-bot-turmas-ao-vivo,
-- no repo dashboard). Decisão do Henrique (25/09): cada consulta bem-sucedida
-- à plataforma (GET /portal/turmas-abertas) grava aqui um retrato de cada
-- turma. Se a consulta falhar, o bot responde pelo retrato: afirma o que não
-- envelhece (a turma existe, horário, início, fim) e põe ressalva nas vagas
-- quando o retrato é antigo. Turma com fim vencido ou que sumiu da consulta
-- nunca é afirmada.
--
-- sumiu_em em vez de DELETE: a turma que deixou de vir (retirada do bot,
-- encerrada) continua registrada, e o bot sabe que NÃO pode falar dela.
-- Sem RLS, como as tabelas desde a 0034: o acesso filtra tenant_id na query.

CREATE TABLE IF NOT EXISTS bot_turma_retrato (
  tenant_id     uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  turma_id      bigint      NOT NULL,            -- class.id do Portal
  nome          text        NOT NULL,
  curso         text        NOT NULL DEFAULT '',
  horarios      jsonb       NOT NULL DEFAULT '[]'::jsonb, -- [{diaSemana, horaInicio, horaFim}]
  inicio        date        NOT NULL,
  fim_previsto  date        NOT NULL,
  alunos        integer     NOT NULL,
  capacidade    integer     NOT NULL,
  vagas         integer     NOT NULL,
  capturado_em  timestamptz NOT NULL,
  sumiu_em      timestamptz,
  PRIMARY KEY (tenant_id, turma_id)
);

COMMENT ON TABLE bot_turma_retrato IS
  'Último retrato de cada turma lido da plataforma. Serve de reserva quando a consulta ao vivo falha.';
