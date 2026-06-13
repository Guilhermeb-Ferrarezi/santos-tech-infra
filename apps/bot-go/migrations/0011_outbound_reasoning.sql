-- Armazena o raciocínio estruturado do LLM por balão de saída.
-- Preenchido somente no primeiro balão de cada resposta (os demais ficam null).
ALTER TABLE outbound_message
    ADD COLUMN IF NOT EXISTS reasoning jsonb;
