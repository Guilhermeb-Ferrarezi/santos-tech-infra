# Design — Bot responde em áudio (espelho): STT + TTS no WhatsApp (Meta)

> Data: 2026-06-15 · Repo: `santos-tech-infra/apps/bot-go` · Canal: **Meta Cloud API**

## Objetivo
Quando o cliente manda uma **mensagem de voz**, o bot (1) **entende** o áudio (STT), responde
com a mesma IA de hoje e (2) devolve a resposta como **nota de voz** (TTS). Modo **espelho**:
áudio do cliente → áudio do bot; texto do cliente → texto do bot (comportamento atual intacto).

Tudo **local** (sem custo por mensagem): `whisper.cpp` (STT) + `Piper` (TTS) + `ffmpeg`,
rodando no próprio container do `bot-go`.

## Estado atual (relevante)
- Inbound de áudio é parseado (`ParseMetaWebhook`) mas **nunca transcrito**: guarda só
  `wa_media_id:<id>` em `Content.MediaURL`; `Content.Transcript` fica `nil` e o engine resolve
  `inboundText` vazio → hoje o bot **ignora voz**.
- `MessageContent` já tem `Type`, `MediaURL`, `MimeType`, `Transcript` (`types.go`).
- `contentToBody` (`whatsapp.go`) já trata `type:"audio"` mas só via `link` (não por media id).
- LLM via `agent-go` recebe **texto**; não vê mídia. STT precisa acontecer no `bot-go` antes.
- Não há ffmpeg/whisper/piper/TTS no repo nem credencial de voz.

## Arquitetura
**Binários embutidos no `bot-go`** (não um sidecar): `ffmpeg`, `whisper.cpp` (`whisper-cli`),
`piper`. O Go invoca via `os/exec`. Justificativa: 1 imagem, 1 deploy, sem HTTP extra; o
`bot-go` já é quem manuseia a mídia do WhatsApp. Single-tenant não justifica um serviço novo.

**Modelos num volume persistente** (Easypanel), baixados no 1º boot por um script de entrypoint
se ausentes — mantém a imagem enxuta e os modelos persistem entre deploys.
- whisper: `ggml-small-q5_1` (PT, ~180MB) — `WHISPER_MODEL` troca para `base` se precisar.
- Piper: voz PT-BR medium (~65MB).

**Footprint:** ~325MB de modelos/binários; pico **~1–1.5GB RAM** por transcrição (transitório,
só em mensagens de áudio); container recomendado **≥2GB RAM**. Texto não usa nada disso.

Novo pacote-folha `internal`/arquivo `voice.go` no `bot-go` com um `VoiceClient`:
- `Transcribe(ctx, audio []byte, mime string) (string, error)` — ffmpeg→WAV16k→whisper-cli.
- `Synthesize(ctx, text string) (ogg []byte, error)` — piper→WAV→ffmpeg→OGG/Opus.
- `Enabled()` — true se `VOICE_ENABLED=1` e binários/modelos presentes.
Sem dependência de `httpapi`; recebe paths via `Config`.

## Fluxo STT (inbound áudio → texto)
Na ingestão do webhook Meta, **antes** do buffer/`bestText`/`Handle`, quando
`Content.Type=="audio"` e `Transcript==nil` e `voice.Enabled()`:
1. **Baixar a mídia**: `GET graph.facebook.com/v21.0/<media-id>` (Bearer = `MetaAccessToken`)
   → JSON com `url` → `GET url` (mesmo Bearer) → bytes OGG/Opus.
2. `ffmpeg -i in.ogg -ar 16000 -ac 1 out.wav`.
3. `whisper-cli -m <modelo> -l pt -nt -f out.wav` → texto.
4. `inbound.Content.Transcript = &texto`. Engine segue igual (já lê `Transcript`).
- Falha em qualquer passo → loga e segue **sem** transcrição (degrada; não trava).
- Limite de tamanho/duração defensivo no download (ex.: ≤16MB) pra não estourar memória.

## Fluxo TTS (resposta → nota de voz) — só no espelho
- O engine sabe que o inbound era áudio (`inbound.Content.Type=="audio"`); passa um
  `replyAsAudio bool` ao envio.
- **Uma nota de voz por resposta:** junta os `output.Bubbles` num texto único (separador de
  pausa, ex.: `\n`), gera **um** áudio: piper → WAV → `ffmpeg -c:a libopus` → OGG/Opus.
- **Upload Meta**: `POST graph.facebook.com/v21.0/<phoneNumberID>/media`
  (`messaging_product=whatsapp`, arquivo `audio/ogg`) → `media id`.
- **Enviar**: `OutboundMessage` com `Content{Type:"audio", MediaURL:"wa_media_id:<id>"}`.
  `contentToBody` passa a usar `audio.id` quando `MediaURL` tem o prefixo `wa_media_id:`,
  senão mantém `link` (compat).
- Persistência: 1 `outbound_message` (`content` JSONB type=audio, com o texto também em
  `Transcript` pra histórico/dashboard legível). **Não guardamos o áudio** — quem hospeda é o
  Meta; ficamos só com o media id + texto. Idempotência: chave por resposta (`<wamid>:voice`),
  exactly-once como hoje.
- Falha no TTS/upload → **fallback para texto** (envia os balões normais). Nunca fica mudo.

**Arquivos temporários:** STT e TTS escrevem em diretório temp (`os.MkdirTemp`/`os.CreateTemp`)
e fazem `defer os.RemoveAll` — nada acumula em disco. O .ogg final é uns KB (~2–3 KB/s de fala);
o WAV intermediário do Piper é maior (~44 KB/s) mas transitório e apagado após o upload.

## Config (sem credencial externa — tudo local)
Novos campos em `config.go` (env): `VOICE_ENABLED` (bool, default false),
`WHISPER_BIN`, `WHISPER_MODEL` (path), `PIPER_BIN`, `PIPER_VOICE` (path), `FFMPEG_BIN`
(defaults sensatos apontando pro volume). Reusa `MetaAccessToken`/`MetaPhoneNumberID` p/
download e upload de mídia.

## Não-escopo (YAGNI)
- Evolution (só Meta agora). Toggle no dashboard (fica no env `VOICE_ENABLED`; UI depois).
- Transcrição persistida como entrada editável; múltiplas vozes; SSML; clonagem de voz.

## Testes
- **Unitários (sem binários):**
  - `replyAsAudio` = (inbound.Content.Type == "audio") — decisão correta.
  - junção de bubbles em texto único (ordem, separador, vazio).
  - `contentToBody`: `MediaURL="wa_media_id:X"` → `{type:audio, audio:{id:X}}`;
    `MediaURL="https://…"` → `{audio:{link:…}}`.
  - parse do JSON de upload Meta (`{id:"..."}`) e do lookup de mídia (`{url:"..."}`) com `httptest`.
- **Integração/manual no servidor** (precisa dos binários/modelos): mandar um áudio real e
  conferir transcrição + resposta em voz; medir latência; validar fallback (desligar
  `VOICE_ENABLED` → volta a texto; forçar erro de TTS → texto).
- `gofmt`/`go vet`/`go test ./...` verdes.

## Dockerfile / deploy
- Instalar `ffmpeg` + baixar/compilar `whisper.cpp` e `piper` na imagem do `bot-go`
  (binários; modelos NÃO — vão no volume).
- Entrypoint: se os modelos não existem no volume, baixar (whisper small q5 + voz piper pt-BR).
- Easypanel: adicionar volume persistente p/ os modelos e garantir RAM ≥2GB no serviço.
- Documentar no `llms.txt` central que o bot agora aceita/produz voz (espelho, Meta).

## Ordem de entrega sugerida
1. STT (entender voz) — fecha a lacuna atual sozinho, testável isolado.
2. TTS + upload Meta + `contentToBody` por id.
3. Dockerfile/entrypoint/volume + verificação no servidor.
