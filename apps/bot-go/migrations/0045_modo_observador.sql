-- Follow-up fase 4: modo observador (observador.go).
--
-- Com um humano atendendo, o bot não responde — mas, com isto ligado, lê a
-- mensagem do cliente para não perder um pedido de retorno ("me chama em
-- dezembro") nem um compromisso ("vou falar com meu marido"). Desligado por
-- padrão: gasta uma consulta curta por mensagem de cliente, e ligar é escolha
-- de alguém na tela WhatsApp · Configurações.
ALTER TABLE tenant_config
  ADD COLUMN IF NOT EXISTS observador_ligado boolean NOT NULL DEFAULT false;

-- Compromissos que o cliente disse, um por linha ("Compromisso (25/09): vai
-- falar com o marido"). Coluna própria de propósito: `observacoes` é regravada
-- pelo bot a partir do que ele leu no começo do atendimento, e uma linha
-- gravada por fora nesse meio-tempo se perderia.
-- Só cresce; quem limpa é gente.
ALTER TABLE lead_qualificacao
  ADD COLUMN IF NOT EXISTS compromissos text NOT NULL DEFAULT '';
