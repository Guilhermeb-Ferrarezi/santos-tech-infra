-- Claim de eventos do outbox por réplica.
--
-- O Drain fazia SELECT ... FOR UPDATE SKIP LOCKED em autocommit (pool.Query):
-- o lock morria ao terminar a leitura, ANTES do processEvent. Com mais de uma
-- réplica (ou durante rolling deploy) o mesmo evento era drenado duas vezes —
-- notificação duplicada, entrada de KB gravada em dobro.
--
-- claimed_at marca o instante em que uma réplica pegou o evento. O Drain só
-- considera eventos sem claim ou com claim vencido (lease), então:
--   • duas réplicas nunca pegam o mesmo evento ao mesmo tempo;
--   • se a réplica morrer no meio, o lease vence e o evento volta à fila
--     (diferente de marcar processed_at no claim, que perderia o evento).
--
-- O índice parcial idx_domain_events_unprocessed (0001) continua servindo o
-- poller: filtra processed_at IS NULL e ordena por occurred_at; claimed_at é
-- predicado residual sobre um conjunto já pequeno.
ALTER TABLE domain_events
  ADD COLUMN IF NOT EXISTS claimed_at timestamptz;

COMMENT ON COLUMN domain_events.claimed_at IS
  'Instante do claim pela réplica que está processando. Lease: expira e o evento volta à fila.';
