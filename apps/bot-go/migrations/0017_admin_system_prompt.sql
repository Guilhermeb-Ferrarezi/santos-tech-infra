-- Prompt do modo administrador, customizável por tenant.
-- Quando preenchido, substitui a parte editável (comportamento/tom/fontes/
-- responsabilidades) do buildAdminPrompt. As partes mecânicas (clientes
-- pendentes, formato de saída, histórico, mensagem atual) continuam fixas.
ALTER TABLE tenant_config
    ADD COLUMN IF NOT EXISTS admin_system_prompt text NOT NULL DEFAULT '';
