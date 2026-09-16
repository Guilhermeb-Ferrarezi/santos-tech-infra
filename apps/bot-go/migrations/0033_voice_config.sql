-- Voz por tenant: escolher provedor e voz pelo painel, sem redeploy.
--
-- Provedores:
--   openai     — sintetiza cada resposta (comportamento atual)
--   clips      — envia áudio pré-gravado do banco; sem clipe para a intenção,
--                responde em texto e registra a lacuna (ver 0034)
--   elevenlabs — sintetiza via ElevenLabs (exige plano com a voz habilitada)
--
-- Defaults preservam EXATAMENTE o comportamento atual: 'openai' com voice_id
-- vazio (= cai no OPENAI_TTS_VOICE do ambiente).
--
-- VOICE_ENABLED (env) continua sendo o interruptor geral: se estiver off, nenhum
-- tenant responde em áudio, independente desta coluna.
ALTER TABLE tenant_config
  ADD COLUMN IF NOT EXISTS voice_enabled  boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS voice_provider text    NOT NULL DEFAULT 'openai',
  ADD COLUMN IF NOT EXISTS voice_id       text    NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS voice_model    text    NOT NULL DEFAULT '';

-- Só os provedores implementados. Constraint nomeada e idempotente para a
-- migração poder rodar de novo no boot sem estourar.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'tenant_config_voice_provider_chk'
  ) THEN
    ALTER TABLE tenant_config
      ADD CONSTRAINT tenant_config_voice_provider_chk
      CHECK (voice_provider IN ('openai', 'elevenlabs', 'clips'));
  END IF;
END $$;

COMMENT ON COLUMN tenant_config.voice_id IS
  'openai: nome da voz (nova, shimmer...). elevenlabs: voice_id. clips: pasta do banco (ex.: henrique).';
