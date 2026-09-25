-- Separar "recebi a mensagem" de "consegui responder".
--
-- O BUG QUE ISTO CORRIGE, observado em produção em 25/09/2026:
--
-- A marca de deduplicação era gravada DENTRO da transação (engine.go), que
-- confirma ANTES de o modelo ser chamado. Quando o serviço de IA devolveu 502,
-- a mensagem já estava marcada como recebida — e o retry, ao reprocessar, leu
-- essa marca, concluiu "duplicada" e desistiu. Pior: logou SUCESSO.
--
-- O cliente escreveu e nunca foi respondido. Ninguém ficou sabendo. Numa escola
-- que vive de lead, cada ocorrência dessas é uma matrícula que não aconteceu, e
-- uma instabilidade momentânea do modelo basta para causá-la.
--
-- Com a coluna nova, a pergunta que o dedup faz muda de "já vi esta mensagem?"
-- para "já RESPONDI esta mensagem?". Reentrega do WhatsApp de algo já
-- respondido continua sendo ignorada; mensagem recebida e não respondida volta
-- a ser processável, que é exatamente o que o retry precisa.
--
-- NULL nas linhas antigas é o que se quer: elas são passado e não serão
-- reprocessadas (o WhatsApp não reentrega mensagem de dias atrás).

ALTER TABLE inbound_message
  ADD COLUMN IF NOT EXISTS respondida_em timestamptz;

-- O retry procura por mensagens recebidas e ainda sem resposta.
CREATE INDEX IF NOT EXISTS inbound_message_sem_resposta_idx
  ON inbound_message (tenant_id, received_at)
  WHERE respondida_em IS NULL;
