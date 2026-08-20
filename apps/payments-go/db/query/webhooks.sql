-- name: ClaimWebhookEvent :execrows
-- Fase 1 do processamento em duas fases. Reivindica o evento gravando-o como
-- 'pending' — o EFEITO AINDA NÃO ACONTECEU. Só depois do efeito aplicado o
-- handler chama MarkWebhookDone.
--
-- Antes, o evento era consumido (INSERT ... DO NOTHING) ANTES do efeito: um
-- timeout da Efí ou um erro de banco descartava o pagamento para sempre, porque
-- a reentrega caía no "já visto" e virava no-op.
--
-- n = 1 → reivindicado, processe. n = 0 → já concluído ('done') ou reivindicado
-- há menos de 1 minuto por outra entrega em voo (evita efeito duplicado).
-- Um 'pending' vencido (tentativa anterior que falhou) É reivindicado de novo.
INSERT INTO pay_webhook_events (id, type, payload, status)
VALUES ($1, $2, $3, 'pending')
ON CONFLICT (id) DO UPDATE
  SET type = EXCLUDED.type, payload = EXCLUDED.payload, processed_at = now()
  WHERE pay_webhook_events.status <> 'done'
    AND pay_webhook_events.processed_at < now() - interval '1 minute';

-- name: MarkWebhookDone :exec
-- Fase 2: o efeito foi aplicado. A partir daqui a reentrega é no-op de propósito.
UPDATE pay_webhook_events SET status = 'done', processed_at = now() WHERE id = $1;
