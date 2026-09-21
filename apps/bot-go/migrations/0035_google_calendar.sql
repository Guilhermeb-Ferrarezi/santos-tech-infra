-- Autorizações do Google Agenda e o vínculo entre aula e evento.
--
-- Por que duas tabelas: a autorização é POR PESSOA (Rodrigo e Henrique cada um
-- autoriza a própria conta) e o evento é POR AULA em cada agenda — a mesma aula
-- vira dois eventos, um em cada calendário, com ids diferentes. Sem guardar o
-- par (aula, pessoa) → id do evento, não há como cancelar nem remarcar depois.

CREATE TABLE IF NOT EXISTS google_calendar_account (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  email         text NOT NULL,              -- a conta que autorizou
  refresh_token text NOT NULL,              -- não expira: o app está "Em produção"
  calendar_id   text NOT NULL DEFAULT 'primary',
  active        boolean NOT NULL DEFAULT true,
  last_error    text,                       -- última falha, para o painel mostrar
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, email)
);

-- Quem recebe os eventos. Consultada a cada agendamento.
CREATE INDEX IF NOT EXISTS google_calendar_account_ativos_idx
  ON google_calendar_account (tenant_id)
  WHERE active;

CREATE TABLE IF NOT EXISTS google_calendar_event (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  account_id     uuid NOT NULL REFERENCES google_calendar_account (id) ON DELETE CASCADE,
  -- A aula no Notion. É a chave que liga os dois mundos: quando a aula é
  -- cancelada ou remarcada lá, é por aqui que se acha o evento a mexer.
  notion_page_id text NOT NULL,
  event_id       text NOT NULL,             -- id do evento no Google
  starts_at      timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, account_id, notion_page_id)
);

CREATE INDEX IF NOT EXISTS google_calendar_event_por_aula_idx
  ON google_calendar_event (tenant_id, notion_page_id);
