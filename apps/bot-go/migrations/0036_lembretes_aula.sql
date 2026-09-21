-- Lembretes da aula experimental para o CLIENTE.
--
-- Separado dos lembretes do Google Agenda de propósito: aqueles avisam quem
-- opera a escola; estes conversam com quem vai fazer a aula, e podem virar
-- remarcação. São públicos diferentes e canais diferentes.
--
-- Uma linha por (aula, momento). O UNIQUE é o que impede o worker de mandar o
-- mesmo lembrete duas vezes se rodar duas réplicas ou reprocessar.

CREATE TABLE IF NOT EXISTS booking_reminder (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  -- A aula no Notion. Cancelou a aula, cancelam-se os lembretes.
  notion_page_id text NOT NULL,
  conversation_id uuid REFERENCES conversation (id) ON DELETE SET NULL,
  client_phone   text NOT NULL,
  channel        text NOT NULL DEFAULT 'whatsapp',
  aluno          text NOT NULL DEFAULT '',
  -- 'vespera' (1 dia antes) | 'quatro_horas' | 'uma_hora'
  kind           text NOT NULL,
  aula_em        timestamptz NOT NULL,
  enviar_em      timestamptz NOT NULL,
  enviado_em     timestamptz,
  -- 'pendente' | 'enviado' | 'cancelado' | 'falhou'
  status         text NOT NULL DEFAULT 'pendente',
  tentativas     int  NOT NULL DEFAULT 0,
  last_error     text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, notion_page_id, kind)
);

-- Busca quente do worker: "o que está vencido e ainda não saiu?"
CREATE INDEX IF NOT EXISTS booking_reminder_fila_idx
  ON booking_reminder (tenant_id, enviar_em)
  WHERE status = 'pendente';

CREATE INDEX IF NOT EXISTS booking_reminder_por_aula_idx
  ON booking_reminder (tenant_id, notion_page_id);
