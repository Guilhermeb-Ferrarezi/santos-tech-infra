-- Follow-up fase 3: o responsável do lead passa a ser uma CONTA da plataforma.
--
-- `lead.owner` era texto livre ("Rodrigo"): não dá para mandar aviso para um
-- nome. `owner_user_id` é o id da conta no auth central (users.id do api-go —
-- outro banco lógico, por isso sem FK). NULL = ninguém atribuído; aí o retorno
-- vai para o responsável padrão da tela (tenant_config.followup_responsavel_id).
-- O texto antigo continua onde está e é exibido enquanto ninguém trocar.
ALTER TABLE lead ADD COLUMN IF NOT EXISTS owner_user_id int;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'lead_owner_user_id_valido') THEN
    ALTER TABLE lead ADD CONSTRAINT lead_owner_user_id_valido
      CHECK (owner_user_id IS NULL OR owner_user_id > 0);
  END IF;
END $$;
