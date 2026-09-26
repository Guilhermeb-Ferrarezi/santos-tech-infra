-- Origem "claude" nas fichas do playbook (pedido do Henrique, 26/09/2026: "a
-- ficha da Vivian aparece sem etiqueta e eu não sabia de onde tinha vindo").
--
-- É uma ANOTAÇÃO: o Claude escreve pela sessão do Henrique, então o servidor
-- não tem como provar quem digitou — como o "avaliou" do Pós-aula. "ia" e
-- "whatsapp" continuam só do servidor. Só amplia a lista de valores: nada é
-- apagado nem reescrito.
ALTER TABLE bot_playbook_situacao DROP CONSTRAINT IF EXISTS bot_playbook_origem_valida;
ALTER TABLE bot_playbook_situacao ADD CONSTRAINT bot_playbook_origem_valida
  CHECK (origem IN ('manual', 'ia', 'whatsapp', 'claude'));
