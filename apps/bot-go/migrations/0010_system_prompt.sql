-- Prompt de sistema customizável por tenant.
-- Quando preenchido, substitui a seção de identidade/persona do BuildPrompt.
ALTER TABLE tenant_config
    ADD COLUMN IF NOT EXISTS system_prompt text NOT NULL DEFAULT '';
