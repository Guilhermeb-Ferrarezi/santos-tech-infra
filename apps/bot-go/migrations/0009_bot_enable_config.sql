-- Migration #9 — Controle de habilitação do bot por tenant
-- bot_enabled_by_default: se false, novas conversas começam com bot desligado.
-- bot_allowed_numbers: lista jsonb de números E.164 que têm bot ativo mesmo quando
-- bot_enabled_by_default=false (ex.: ["5516991230000","5511999998888"]).

ALTER TABLE tenant_config
  ADD COLUMN bot_enabled_by_default boolean NOT NULL DEFAULT true,
  ADD COLUMN bot_allowed_numbers    jsonb   NOT NULL DEFAULT '[]'::jsonb;
