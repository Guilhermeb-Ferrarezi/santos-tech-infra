-- ============================================================================
-- Migration #23 — canal de origem do CLIENTE em pending_question / pending_booking
-- ----------------------------------------------------------------------------
-- Bug: o reply ao cliente (executeClientActions / executeBookingActions) usava o
-- canal da conversa do ADMIN para escolher o sender. Resultado: respostas a um
-- cliente que veio pela Evolution saíam pelo número OFICIAL (Meta) — número errado.
--
-- Fix: cada pendência passa a carregar o canal de origem do cliente. O engine
-- grava esse canal ao inserir a pendência e, ao responder, escolhe o sender
-- correspondente (Meta para 'whatsapp', Evolution para 'evolution').
--
-- Default 'whatsapp' preserva o comportamento das linhas existentes (que vieram
-- todas do número oficial até aqui). Idempotente (ADD COLUMN IF NOT EXISTS).
-- ============================================================================

ALTER TABLE pending_question
  ADD COLUMN IF NOT EXISTS channel text NOT NULL DEFAULT 'whatsapp';

ALTER TABLE pending_booking
  ADD COLUMN IF NOT EXISTS channel text NOT NULL DEFAULT 'whatsapp';

COMMENT ON COLUMN pending_question.channel IS
  'Canal de origem do CLIENTE (whatsapp | evolution). Define por qual sender a resposta sai.';
COMMENT ON COLUMN pending_booking.channel IS
  'Canal de origem do CLIENTE (whatsapp | evolution). Define por qual sender o aviso de agendamento sai.';
