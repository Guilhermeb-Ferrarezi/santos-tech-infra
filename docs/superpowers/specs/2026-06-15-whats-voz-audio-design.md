# Design — Bot responde em áudio (espelho): STT + TTS via OpenAI (Meta)

> Data: 2026-06-15 · Repo: `santos-tech-infra/apps/bot-go` · Canal: **Meta Cloud API**
> Provedor de voz: **OpenAI** (STT `/v1/audio/transcriptions` + TTS `/v1/audio/speech`).

## Objetivo
Quando o cliente manda uma **mensagem de voz**, o bot (1) **entende** o áudio (STT), responde
com a mesma IA de hoje e (2) devolve a resposta como **nota de voz** com **voz feminina** (TTS).
Modo **espelho**: áudio → áudio; texto → texto (comportamento atual intacto).

## Provedor: OpenAI (decisão)
O usuário já tem `OPENAI_API_KEY`. Usar a OpenAI nas duas pontas **elimina toda a infra local**
(sem whisper.cpp/Piper/ffmpeg/modelos/volume/RAM extra) — só chamadas HTTP em Go. A imagem do
`bot-go` **não muda** (segue `distroless/static`). Piper não tem voz feminina pt-BR; OpenAI tem.
- **STT:** `POST https://api.openai.com/v1/audio/transcriptions`, `model=whisper-1`,
  `language=pt`, arquivo = o OGG/Opus baixado do WhatsApp (aceito direto, sem transcode).
- **TTS:** `POST https://api.openai.com/v1/audio/speech`, `model=gpt-4o-mini-tts`,
  `voice=nova` (feminina; trocável), `response_format=opus` → bytes OGG/Opus prontos p/ nota de voz.
- **Custo:** STT ~US$0,006/min; TTS ~US$0,002/resposta. Desprezível no volume.

## Estado atual (relevante)
- Inbound de áudio é parseado (`ParseMetaWebhook`) mas **nunca transcrito**: guarda só
  `wa_media_id:<id>` em `Content.MediaURL`; `Transcript` fica `nil` → hoje o bot **ignora voz**.
- `MessageContent` já tem `Type`, `MediaURL`, `MimeType`, `Transcript`.
- `contentToBody` (`whatsapp.go`) trata `type:"audio"` mas só via `link` (não por media id).
- LLM via `agent-go` recebe **texto**; STT precisa acontecer no `bot-go` antes.

## Arquitetura
Novo `VoiceClient` (`voice.go`) com um `http.Client`, sem dependência de `httpapi`:
- `Transcribe(ctx, audio []byte, mime string) (string, error)` — multipart p/ OpenAI STT.
- `Synthesize(ctx, text string) (ogg []byte, error)` — JSON p/ OpenAI TTS (`response_format=opus`).
- `Enabled()` — `VOICE_ENABLED=1` && `OPENAI_API_KEY != ""`.

## Fluxo STT (inbound áudio → texto)
Na ingestão do webhook Meta, **antes** do buffer/`bestText`/`Handle`, quando
`Content.Type=="audio"` && `Transcript==nil` && `voice.Enabled()`:
1. **Baixar a mídia**: `GET graph/<media-id>` (Bearer `MetaAccessToken`) → `{url}` → `GET url` → bytes.
2. `voice.Transcribe(audio, mime)` → texto PT (OpenAI aceita OGG direto).
3. `inbound.Content.Transcript = &texto`; `inbound.WasVoice = true`. Engine segue igual.
- Falha em qualquer passo → loga e segue **sem** transcrição (degrada; não trava).
- Teto de download defensivo (≤16MB).

## Fluxo TTS (resposta → nota de voz) — só no espelho
- Engine sabe que o inbound era voz (`WasVoice`). **Uma nota de voz por resposta:** junta os
  `Bubbles` num texto único → `voice.Synthesize` → OGG/Opus.
- **Upload Meta**: `POST graph/<phoneNumberID>/media` (`audio/ogg`) → media id.
- **Enviar**: `OutboundMessage{Content:{Type:"audio", MediaURL:"wa_media_id:<id>", Transcript:&text}}`.
  `contentToBody` passa a usar `audio.id` quando `MediaURL` tem prefixo `wa_media_id:` (senão `link`).
- Persistência: 1 `outbound_message` gravando o **texto** (dashboard legível). **Não guardamos o
  áudio** — Meta hospeda; ficamos com media id + texto. Idempotência `<wamid>:voice`.
- Falha no TTS/upload → **fallback para texto** (envia os balões normais). Nunca fica mudo.
- Só canal Meta (type-assert do sender p/ `*WhatsAppSender`; Evolution cai pra texto).

## Config (env)
- `VOICE_ENABLED` (bool, default false), `OPENAI_API_KEY`, `OPENAI_TTS_VOICE` (default `nova`),
  `OPENAI_TTS_MODEL` (default `gpt-4o-mini-tts`), `OPENAI_STT_MODEL` (default `whisper-1`),
  `OPENAI_BASE_URL` (default `https://api.openai.com/v1`, **injetável p/ testes httptest**).
- Reusa `MetaAccessToken`/`MetaPhoneNumberID` p/ download e upload de mídia.

## Não-escopo (YAGNI)
- Evolution (só Meta). Toggle no dashboard (fica no env; UI depois). STT/TTS local. Múltiplas vozes na UI.

## Testes
- **Unitários (httptest, sem binários):**
  - `VoiceClient.Transcribe`: mock do endpoint OpenAI STT → retorna `{text:"..."}`; valida multipart e parse.
  - `VoiceClient.Synthesize`: mock do endpoint TTS → bytes; valida body (model/voice/format) e retorno.
  - `WhatsAppSender.DownloadMedia` / `UploadAudio`: httptest da Graph API.
  - `contentToBody`: `MediaURL="wa_media_id:X"` → `{audio:{id:X}}`; URL http → `{audio:{link}}`.
  - `shouldReplyAsAudio` / `joinBubbles`: funções puras.
- **Manual/integração** (com `OPENAI_API_KEY` real): mandar áudio real → conferir transcrição +
  resposta em voz feminina; validar fallback (`VOICE_ENABLED=0` ou key inválida → texto).
- `gofmt`/`go vet`/`go test ./...` verdes.

## Deploy
- **Sem mudança de Dockerfile.** Basta setar no Easypanel: `VOICE_ENABLED=true` + `OPENAI_API_KEY`
  (+ opcional `OPENAI_TTS_VOICE`). Documentar no `.env.example` e no `llms.txt` central.

## Ordem de entrega
1. STT (entender voz) — fecha a lacuna atual sozinho.
2. TTS + upload Meta + `contentToBody` por id + hook no engine.
3. `.env.example`/docs + verificação manual.
