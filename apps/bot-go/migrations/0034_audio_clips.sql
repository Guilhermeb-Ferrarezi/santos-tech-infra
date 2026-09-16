-- Banco de áudios pré-gravados + registro das lacunas.
--
-- Contexto: a voz do atendente foi clonada enquanto havia plano de TTS. O plano
-- acabou e não será renovado, então NÃO é possível sintetizar fala nova nessa
-- voz — só existem os arquivos já gerados. Este par de tabelas é o que permite
-- usar esse acervo e fazê-lo crescer de forma guiada.

-- audio_clips: um registro por ARQUIVO (intenção + variante).
-- O arquivo em si vive em disco (AUDIO_CLIPS_DIR), não no banco.
CREATE TABLE IF NOT EXISTS audio_clips (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  voice       text NOT NULL,              -- pasta da voz: 'henrique', 'julia'...
  intent_key  text NOT NULL,              -- ex.: 'conv_experimental'
  variant     integer NOT NULL DEFAULT 1, -- 1, 2, 3... mesma intenção, palavras diferentes
  file_path   text NOT NULL,              -- relativo a AUDIO_CLIPS_DIR
  transcript  text NOT NULL DEFAULT '',   -- o que o áudio fala (para log e auditoria)
  duration_ms integer NOT NULL DEFAULT 0,
  category    text NOT NULL DEFAULT '',
  active      boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, voice, intent_key, variant)
);

-- Busca quente: "me dá as variantes ativas desta intenção nesta voz".
CREATE INDEX IF NOT EXISTS audio_clips_lookup_idx
  ON audio_clips (tenant_id, voice, intent_key)
  WHERE active;

-- audio_gaps: momentos em que o bot QUIS falar e não tinha clipe.
--
-- É o que faz o banco crescer guiado por demanda real em vez de palpite: a
-- lacuna vira uma fila de gravação. `hits` conta quantas vezes aquela mesma
-- falta aconteceu — as mais frequentes são as que valem gravar primeiro.
CREATE TABLE IF NOT EXISTS audio_gaps (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  voice        text NOT NULL,
  intent_key   text NOT NULL DEFAULT '',  -- vazio = não havia intenção mapeada
  sample_text  text NOT NULL,             -- o texto que o bot teria falado
  hits         integer NOT NULL DEFAULT 1,
  resolved     boolean NOT NULL DEFAULT false,
  first_seen   timestamptz NOT NULL DEFAULT now(),
  last_seen    timestamptz NOT NULL DEFAULT now()
);

-- Dedup por (voz, intenção, texto): a mesma lacuna incrementa `hits` em vez de
-- criar linha nova. Com intent_key vazio, o texto é o que distingue.
CREATE UNIQUE INDEX IF NOT EXISTS audio_gaps_dedup_idx
  ON audio_gaps (tenant_id, voice, intent_key, md5(sample_text));

CREATE INDEX IF NOT EXISTS audio_gaps_pendentes_idx
  ON audio_gaps (tenant_id, hits DESC)
  WHERE NOT resolved;
