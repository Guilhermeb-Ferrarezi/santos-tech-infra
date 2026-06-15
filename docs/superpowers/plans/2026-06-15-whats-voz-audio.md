# Bot responde em áudio (espelho) — STT + TTS — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Quando o cliente manda voz no WhatsApp (Meta), o bot transcreve (whisper.cpp), responde com a IA atual e devolve a resposta como nota de voz (Piper), tudo local.

**Architecture:** Binários (`ffmpeg`, `whisper-cli`, `piper`) embutidos na imagem do `bot-go`; modelos num volume persistente. Um `VoiceClient` (voice.go) encapsula STT/TTS via `os/exec`. STT acontece na ingestão do webhook (preenche `Transcript`); TTS acontece no envio quando o inbound foi voz (espelho), com **fallback para texto** em qualquer falha.

**Tech Stack:** Go 1.24, Meta Cloud API (Graph v21.0), whisper.cpp, Piper, ffmpeg.

**Caminho:** `/home/guilherme/projetos/sg/santos-tech-infra/apps/bot-go`. Go em `~/.local/bin` (`export PATH="$HOME/.local/bin:$PATH"`). Branch já criada: `feat/whats-voz-audio`.

**Testes (realidade do repo):** sem harness de DB; binários de áudio não rodam em CI. TDD aplica-se às **funções puras** (montagem de comando, junção de balões, seleção id/link) e ao HTTP do Meta (httptest). Execução real de STT/TTS = verificação manual no servidor (Fase C).

---

## FASE A — STT (entender a voz do cliente)

### Task A1: Config de voz

**Files:** `config.go`

- [ ] **Step 1: Adicionar campos ao struct `Config`** (após o bloco "Dashboard"):

```go
	// Voz (STT/TTS local). Tudo opcional; VOICE_ENABLED liga a feature.
	VoiceEnabled bool
	WhisperBin   string
	WhisperModel string
	PiperBin     string
	PiperVoice   string
	FFmpegBin    string
```

- [ ] **Step 2: Preencher em `LoadConfig`** (antes do fechamento do `return Config{...}`):

```go
		VoiceEnabled: getEnv("VOICE_ENABLED", "false") == "true",
		WhisperBin:   getEnv("WHISPER_BIN", "whisper-cli"),
		WhisperModel: getEnv("WHISPER_MODEL", "/models/ggml-small-q5_1.bin"),
		PiperBin:     getEnv("PIPER_BIN", "piper"),
		PiperVoice:   getEnv("PIPER_VOICE", "/models/pt_BR-faber-medium.onnx"),
		FFmpegBin:    getEnv("FFMPEG_BIN", "ffmpeg"),
```

- [ ] **Step 3:** `export PATH="$HOME/.local/bin:$PATH" && go build ./...` → sem erro.
- [ ] **Step 4: Commit** `git add config.go && git commit -m "feat(bot): config de voz (STT/TTS local)"`

---

### Task A2: VoiceClient — STT (voice.go) com montagem de comando testável

**Files:** Create `voice.go`, Test `voice_test.go`

- [ ] **Step 1: Teste das funções puras de montagem de args** (`voice_test.go`):

```go
package main

import (
	"reflect"
	"testing"
)

func TestWhisperArgs(t *testing.T) {
	got := whisperArgs("/m/model.bin", "/tmp/a.wav")
	want := []string{"-m", "/m/model.bin", "-l", "pt", "-nt", "-otxt", "-of", "/tmp/a", "-f", "/tmp/a.wav"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("whisperArgs = %v, quer %v", got, want)
	}
}

func TestFfmpegToWavArgs(t *testing.T) {
	got := ffmpegToWavArgs("/tmp/in.ogg", "/tmp/out.wav")
	want := []string{"-y", "-i", "/tmp/in.ogg", "-ar", "16000", "-ac", "1", "/tmp/out.wav"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ffmpegToWavArgs = %v, quer %v", got, want)
	}
}
```

- [ ] **Step 2:** `go test ./... -run 'TestWhisperArgs|TestFfmpegToWavArgs'` → FAIL (undefined).

- [ ] **Step 3: Implementar `voice.go`:**

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// VoiceClient encapsula STT (whisper.cpp) e TTS (Piper) via binários locais.
type VoiceClient struct {
	enabled      bool
	whisperBin   string
	whisperModel string
	piperBin     string
	piperVoice   string
	ffmpegBin    string
}

func NewVoiceClient(cfg Config) *VoiceClient {
	return &VoiceClient{
		enabled:      cfg.VoiceEnabled,
		whisperBin:   cfg.WhisperBin,
		whisperModel: cfg.WhisperModel,
		piperBin:     cfg.PiperBin,
		piperVoice:   cfg.PiperVoice,
		ffmpegBin:    cfg.FFmpegBin,
	}
}

func (v *VoiceClient) Enabled() bool { return v != nil && v.enabled }

// whisperArgs monta os argumentos do whisper-cli: PT, sem timestamps, saída em <base>.txt.
func whisperArgs(model, wav string) []string {
	base := strings.TrimSuffix(wav, filepath.Ext(wav))
	return []string{"-m", model, "-l", "pt", "-nt", "-otxt", "-of", base, "-f", wav}
}

// ffmpegToWavArgs converte qualquer áudio em WAV 16kHz mono (formato do whisper).
func ffmpegToWavArgs(in, out string) []string {
	return []string{"-y", "-i", in, "-ar", "16000", "-ac", "1", out}
}

// Transcribe recebe o áudio bruto (OGG/Opus do WhatsApp) e devolve o texto em PT.
// Usa diretório temporário com limpeza garantida (defer RemoveAll).
func (v *VoiceClient) Transcribe(ctx context.Context, audio []byte) (string, error) {
	dir, err := os.MkdirTemp("", "stt-*")
	if err != nil {
		return "", fmt.Errorf("voice: tempdir: %w", err)
	}
	defer os.RemoveAll(dir)

	inPath := filepath.Join(dir, "in.ogg")
	wavPath := filepath.Join(dir, "a.wav")
	if err := os.WriteFile(inPath, audio, 0o600); err != nil {
		return "", fmt.Errorf("voice: write in: %w", err)
	}
	if err := exec.CommandContext(ctx, v.ffmpegBin, ffmpegToWavArgs(inPath, wavPath)...).Run(); err != nil {
		return "", fmt.Errorf("voice: ffmpeg->wav: %w", err)
	}
	if err := exec.CommandContext(ctx, v.whisperBin, whisperArgs(v.whisperModel, wavPath)...).Run(); err != nil {
		return "", fmt.Errorf("voice: whisper: %w", err)
	}
	out, err := os.ReadFile(strings.TrimSuffix(wavPath, ".wav") + ".txt")
	if err != nil {
		return "", fmt.Errorf("voice: read transcript: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
```

- [ ] **Step 4:** `go test ./... -run 'TestWhisperArgs|TestFfmpegToWavArgs'` → PASS.
- [ ] **Step 5:** `go vet ./... && gofmt -l voice.go voice_test.go` (limpo).
- [ ] **Step 6: Commit** `git add voice.go voice_test.go && git commit -m "feat(bot): VoiceClient STT (whisper.cpp via exec)"`

---

### Task A3: Download de mídia do Meta (WhatsAppSender.DownloadMedia)

**Files:** `whatsapp.go`, Test `whatsapp_media_test.go`

- [ ] **Step 1: Teste com httptest** (`whatsapp_media_test.go`):

```go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDownloadMedia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/lookup" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"url":"` + "http://" + r.Host + `/bytes","mime_type":"audio/ogg"}`))
			return
		}
		_, _ = w.Write([]byte("OGGDATA"))
	}))
	defer srv.Close()

	s := &WhatsAppSender{accessToken: "tok", http: srv.Client()}
	data, mime, err := s.downloadMediaFrom(context.Background(), srv.URL+"/lookup")
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if string(data) != "OGGDATA" || mime != "audio/ogg" {
		t.Fatalf("got %q %q", data, mime)
	}
}
```

- [ ] **Step 2:** `go test ./... -run TestDownloadMedia` → FAIL (undefined).

- [ ] **Step 3: Implementar em `whatsapp.go`** (após `post`). `DownloadMedia` resolve a URL pelo media id (endpoint Graph) e delega a `downloadMediaFrom` (testável com URL injetada):

```go
// DownloadMedia baixa o conteúdo de uma mídia do WhatsApp a partir do media id.
// Passo 1: GET graph/<id> → { url }. Passo 2: GET url (mesmo Bearer) → bytes.
func (s *WhatsAppSender) DownloadMedia(ctx context.Context, mediaID string) ([]byte, string, error) {
	lookup := fmt.Sprintf("https://graph.facebook.com/v21.0/%s", mediaID)
	return s.downloadMediaFrom(ctx, lookup)
}

func (s *WhatsAppSender) downloadMediaFrom(ctx context.Context, lookupURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lookupURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.accessToken)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("whatsapp: media lookup: %w", err)
	}
	defer resp.Body.Close()
	var meta struct {
		URL      string `json:"url"`
		MimeType string `json:"mime_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, "", fmt.Errorf("whatsapp: media lookup decode: %w", err)
	}
	if meta.URL == "" {
		return nil, "", fmt.Errorf("whatsapp: media sem url")
	}

	dreq, err := http.NewRequestWithContext(ctx, http.MethodGet, meta.URL, nil)
	if err != nil {
		return nil, "", err
	}
	dreq.Header.Set("Authorization", "Bearer "+s.accessToken)
	dresp, err := s.http.Do(dreq)
	if err != nil {
		return nil, "", fmt.Errorf("whatsapp: media download: %w", err)
	}
	defer dresp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(dresp.Body, 16<<20)) // teto 16MB
	if err != nil {
		return nil, "", fmt.Errorf("whatsapp: media read: %w", err)
	}
	return data, meta.MimeType, nil
}
```

- [ ] **Step 4:** `go test ./... -run TestDownloadMedia` → PASS.
- [ ] **Step 5: Commit** `git add whatsapp.go whatsapp_media_test.go && git commit -m "feat(bot): download de mídia do Meta (para STT)"`

---

### Task A4: Campo `WasVoice` no inbound + preservar na combinação

**Files:** `types.go`, `server.go` (combineInbound)

- [ ] **Step 1: Adicionar `WasVoice` em `InboundMessage`** (`types.go`, após `ReceivedAt`):

```go
	ReceivedAt        time.Time
	// WasVoice indica que a mensagem original do cliente era nota de voz (para o
	// modo espelho: responder em áudio). Setado após o STT.
	WasVoice bool
```

- [ ] **Step 2: Preservar em `combineInbound`** (`server.go`): após `base := items[len(items)-1].msg`, marcar se qualquer item da rajada foi voz:

```go
	base := items[len(items)-1].msg
	for _, it := range items {
		if it.msg.WasVoice {
			base.WasVoice = true
			break
		}
	}
```

- [ ] **Step 3:** `go build ./... && go vet ./...` → ok.
- [ ] **Step 4: Commit** `git add types.go server.go && git commit -m "feat(bot): InboundMessage.WasVoice preservado na rajada"`

---

### Task A5: Wire do Voice no Server + hook de STT na ingestão

**Files:** `server.go` (Server struct, NewServer, processInbound), `main.go`

- [ ] **Step 1: Campo `voice` no `Server`** (`server.go`, struct `Server`, junto de `evoClient`):

```go
	evoClient  *EvolutionClient
	voice      *VoiceClient
```

- [ ] **Step 2: Parâmetro no `NewServer`** — adicionar `voice *VoiceClient` ao final da assinatura e setar `voice: voice` no struct literal. Assinatura atual termina em `evoClient *EvolutionClient)`; vira `evoClient *EvolutionClient, voice *VoiceClient)`. No corpo, após `evoClient:  evoClient,` adicionar `voice:      voice,`.

- [ ] **Step 3: Hook de STT em `processInbound`** (`server.go`), logo após o loop começar `for _, msg := range msgs {` e ANTES do `msg.TenantID = ...`/dedup, transcrever áudio:

```go
	for _, msg := range msgs {
		// STT: transcreve nota de voz antes de tudo (preenche Transcript + WasVoice),
		// para o engine entender e o modo espelho responder em áudio. Best-effort.
		if msg.Content.Type == "audio" && msg.Content.Transcript == nil &&
			s.voice.Enabled() && s.sender != nil && msg.Content.MediaURL != nil {
			mediaID := strings.TrimPrefix(*msg.Content.MediaURL, "wa_media_id:")
			if audio, _, derr := s.sender.DownloadMedia(ctx, mediaID); derr != nil {
				s.logger.Error("stt: download mídia", "err", derr)
			} else if text, terr := s.voice.Transcribe(ctx, audio); terr != nil {
				s.logger.Error("stt: transcrição", "err", terr)
			} else if text != "" {
				msg.Content.Transcript = &text
				msg.WasVoice = true
			}
		}

		msg.TenantID = s.cfg.TenantID
```

(`strings` já está importado em server.go.)

- [ ] **Step 4: Construir e passar o Voice no `main.go`** — após `sender := NewWhatsAppSender(...)` (linha ~85):

```go
	voiceClient := NewVoiceClient(cfg)
```

E na chamada `NewServer(...)` (linha ~149+), adicionar `voiceClient` como último argumento, na mesma ordem da nova assinatura.

- [ ] **Step 5:** `go build ./... && go vet ./... && go test ./...` → tudo verde.
- [ ] **Step 6: Commit** `git add server.go main.go && git commit -m "feat(bot): STT na ingestão (transcreve voz do cliente)"`

---

## FASE B — TTS (responder em nota de voz)

### Task B1: VoiceClient.Synthesize + junção de balões

**Files:** `voice.go`, `voice_test.go`

- [ ] **Step 1: Teste das funções puras** (adicionar em `voice_test.go`):

```go
func TestJoinBubbles(t *testing.T) {
	if got := joinBubbles([]string{"Oi!", "Tudo bem?"}); got != "Oi!\nTudo bem?" {
		t.Fatalf("joinBubbles = %q", got)
	}
	if got := joinBubbles([]string{"Só um"}); got != "Só um" {
		t.Fatalf("joinBubbles single = %q", got)
	}
}

func TestFfmpegToOggArgs(t *testing.T) {
	got := ffmpegToOggArgs("/tmp/a.wav", "/tmp/a.ogg")
	want := []string{"-y", "-i", "/tmp/a.wav", "-c:a", "libopus", "-b:a", "24k", "-ar", "48000", "-ac", "1", "/tmp/a.ogg"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ffmpegToOggArgs = %v, quer %v", got, want)
	}
}
```

- [ ] **Step 2:** `go test ./... -run 'TestJoinBubbles|TestFfmpegToOggArgs'` → FAIL.

- [ ] **Step 3: Implementar em `voice.go`:**

```go
// joinBubbles junta os balões num texto único para gerar UMA nota de voz.
func joinBubbles(bubbles []string) string {
	return strings.Join(bubbles, "\n")
}

// ffmpegToOggArgs converte WAV em OGG/Opus (formato de nota de voz do WhatsApp).
func ffmpegToOggArgs(in, out string) []string {
	return []string{"-y", "-i", in, "-c:a", "libopus", "-b:a", "24k", "-ar", "48000", "-ac", "1", out}
}

// Synthesize gera uma nota de voz OGG/Opus a partir do texto. Piper escreve WAV;
// ffmpeg converte para OGG/Opus. Temporários limpos via defer.
func (v *VoiceClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "tts-*")
	if err != nil {
		return nil, fmt.Errorf("voice: tempdir: %w", err)
	}
	defer os.RemoveAll(dir)

	wavPath := filepath.Join(dir, "out.wav")
	oggPath := filepath.Join(dir, "out.ogg")

	piper := exec.CommandContext(ctx, v.piperBin, "--model", v.piperVoice, "--output_file", wavPath)
	piper.Stdin = strings.NewReader(text)
	if err := piper.Run(); err != nil {
		return nil, fmt.Errorf("voice: piper: %w", err)
	}
	if err := exec.CommandContext(ctx, v.ffmpegBin, ffmpegToOggArgs(wavPath, oggPath)...).Run(); err != nil {
		return nil, fmt.Errorf("voice: ffmpeg->ogg: %w", err)
	}
	return os.ReadFile(oggPath)
}
```

- [ ] **Step 4:** `go test ./... -run 'TestJoinBubbles|TestFfmpegToOggArgs'` → PASS.
- [ ] **Step 5: Commit** `git add voice.go voice_test.go && git commit -m "feat(bot): VoiceClient TTS (Piper -> OGG/Opus)"`

---

### Task B2: Upload de áudio no Meta (WhatsAppSender.UploadAudio)

**Files:** `whatsapp.go`, Test `whatsapp_media_test.go`

- [ ] **Step 1: Teste httptest** (adicionar em `whatsapp_media_test.go`):

```go
func TestUploadAudio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct == "" || ct[:19] != "multipart/form-data" {
			t.Errorf("content-type inesperado: %q", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"MEDIA123"}`))
	}))
	defer srv.Close()

	s := &WhatsAppSender{accessToken: "tok", phoneNumberID: "PN", http: srv.Client()}
	id, err := s.uploadAudioTo(context.Background(), srv.URL, []byte("OGG"))
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if id != "MEDIA123" {
		t.Fatalf("id = %q", id)
	}
}
```

- [ ] **Step 2:** `go test ./... -run TestUploadAudio` → FAIL.

- [ ] **Step 3: Implementar em `whatsapp.go`** (precisa `mime/multipart` no import block). `UploadAudio` chama `uploadAudioTo` com a URL real:

```go
// UploadAudio envia bytes OGG/Opus ao endpoint de mídia do Meta e devolve o media id.
func (s *WhatsAppSender) UploadAudio(ctx context.Context, ogg []byte) (string, error) {
	url := fmt.Sprintf("https://graph.facebook.com/v21.0/%s/media", s.phoneNumberID)
	return s.uploadAudioTo(ctx, url, ogg)
}

func (s *WhatsAppSender) uploadAudioTo(ctx context.Context, url string, ogg []byte) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("messaging_product", "whatsapp")
	_ = mw.WriteField("type", "audio/ogg")
	fw, err := mw.CreateFormFile("file", "voice.ogg")
	if err != nil {
		return "", fmt.Errorf("whatsapp: upload form: %w", err)
	}
	if _, err := fw.Write(ogg); err != nil {
		return "", fmt.Errorf("whatsapp: upload write: %w", err)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("whatsapp: upload close: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.accessToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("whatsapp: upload do: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whatsapp: upload status %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("whatsapp: upload sem id: %s", string(raw))
	}
	return out.ID, nil
}
```

- [ ] **Step 4:** `go test ./... -run TestUploadAudio` → PASS.
- [ ] **Step 5: Commit** `git add whatsapp.go whatsapp_media_test.go && git commit -m "feat(bot): upload de áudio no Meta (media id)"`

---

### Task B3: contentToBody — áudio por media id

**Files:** `whatsapp.go`, Test `whatsapp_media_test.go`

- [ ] **Step 1: Teste** (adicionar em `whatsapp_media_test.go`):

```go
func TestContentToBodyAudioByID(t *testing.T) {
	id := "wa_media_id:MEDIA123"
	body := contentToBody(MessageContent{Type: "audio", MediaURL: &id})
	audio := body["audio"].(map[string]any)
	if audio["id"] != "MEDIA123" {
		t.Fatalf("esperava id MEDIA123, got %v", audio)
	}
	if _, hasLink := audio["link"]; hasLink {
		t.Fatalf("não deveria ter link quando é media id")
	}

	link := "https://x/a.ogg"
	body2 := contentToBody(MessageContent{Type: "audio", MediaURL: &link})
	if body2["audio"].(map[string]any)["link"] != "https://x/a.ogg" {
		t.Fatalf("esperava link")
	}
}
```

- [ ] **Step 2:** `go test ./... -run TestContentToBodyAudioByID` → FAIL.

- [ ] **Step 3: Ajustar o case `"audio"` em `contentToBody`:**

```go
	case "audio":
		audio := map[string]any{}
		if content.MediaURL != nil {
			if id, ok := strings.CutPrefix(*content.MediaURL, "wa_media_id:"); ok {
				audio["id"] = id
			} else {
				audio["link"] = *content.MediaURL
			}
		}
		return map[string]any{"type": "audio", "audio": audio}
```

- [ ] **Step 4:** `go test ./... -run TestContentToBodyAudioByID` → PASS.
- [ ] **Step 5: Commit** `git add whatsapp.go whatsapp_media_test.go && git commit -m "feat(bot): contentToBody envia áudio por media id"`

---

### Task B4: Engine — Voice na deps + envio de nota de voz no espelho

**Files:** `engine.go`, `main.go`, Test `engine_voice_test.go`

- [ ] **Step 1: Adicionar `Voice` ao `EngineDeps`** (`engine.go`, após `Notion *NotionClient`):

```go
	// Voice — STT/TTS local (opcional). Quando presente e habilitado, o engine
	// responde em áudio às mensagens que vieram em voz (espelho).
	Voice *VoiceClient
```

- [ ] **Step 2: Teste da decisão pura** (`engine_voice_test.go`):

```go
package main

import "testing"

func TestShouldReplyAsAudio(t *testing.T) {
	on := &VoiceClient{enabled: true}
	off := &VoiceClient{enabled: false}
	if !shouldReplyAsAudio(on, InboundMessage{WasVoice: true}) {
		t.Fatal("voz + habilitado deveria ser true")
	}
	if shouldReplyAsAudio(on, InboundMessage{WasVoice: false}) {
		t.Fatal("texto não deveria virar áudio")
	}
	if shouldReplyAsAudio(off, InboundMessage{WasVoice: true}) {
		t.Fatal("desabilitado deveria ser false")
	}
	if shouldReplyAsAudio(nil, InboundMessage{WasVoice: true}) {
		t.Fatal("nil deveria ser false")
	}
}
```

- [ ] **Step 3:** `go test ./... -run TestShouldReplyAsAudio` → FAIL.

- [ ] **Step 4: Implementar a decisão + o envio de voz no `engine.go`.** Adicionar o helper puro:

```go
// shouldReplyAsAudio: responde em áudio só quando o cliente mandou voz e o TTS está ligado.
func shouldReplyAsAudio(v *VoiceClient, inbound InboundMessage) bool {
	return inbound.WasVoice && v.Enabled()
}
```

E, no início do bloco de envio (logo ANTES do `for i, bubble := range output.Bubbles {`), tentar a nota de voz única; em sucesso, **envolver o loop de texto num `if !sentVoice`** (sem `goto`, evita erro "jumps over declaration"):

```go
	sentVoice := false
	if shouldReplyAsAudio(e.deps.Voice, inbound) && len(output.Bubbles) > 0 {
		sentVoice = e.trySendVoice(ctx, conv, inbound, output, reasoningJSON)
		if !sentVoice {
			log.Info("voz falhou; caindo para texto", "wamid", wamid)
		}
	}

	if !sentVoice {
		for i, bubble := range output.Bubbles {
			// ... corpo atual do loop, inalterado ...
		}
	}
```

Ou seja: indente o `for` existente dentro do `if !sentVoice {`. Não muda nada do corpo do loop — só o envolve. O código após o loop (broadcast/fase 3) continua igual, fora do `if`.

E implementar `trySendVoice` (perto do final do arquivo). Ele sintetiza, faz upload pelo `*WhatsAppSender` (type-assert; só Meta), envia e persiste. Retorna `false` em qualquer falha (→ fallback texto):

```go
// trySendVoice gera UMA nota de voz com a resposta inteira e envia pelo Meta.
// Retorna true se entregou; false (com log) para o chamador cair no texto.
func (e *ConversationEngine) trySendVoice(ctx context.Context, conv Conversation, inbound InboundMessage, output ResponderOutput, reasoningJSON *string) bool {
	log := e.logger()
	ms, ok := e.deps.Sender.(*WhatsAppSender)
	if !ok {
		return false // só Meta suporta upload de mídia
	}
	text := joinBubbles(output.Bubbles)
	ogg, err := e.deps.Voice.Synthesize(ctx, text)
	if err != nil {
		log.Error("tts: synthesize", "err", err)
		return false
	}
	mediaID, err := ms.UploadAudio(ctx, ogg)
	if err != nil {
		log.Error("tts: upload", "err", err)
		return false
	}
	ref := "wa_media_id:" + mediaID
	idemKey := fmt.Sprintf("%s:voice", inbound.ProviderMessageID)
	out := OutboundMessage{
		TenantID:       inbound.TenantID,
		ConversationID: conv.ID,
		Channel:        inbound.Channel,
		To:             inbound.ExternalID,
		Intent:         IntentFreeForm,
		Content:        MessageContent{Type: "audio", MediaURL: &ref, Transcript: &text},
		IdempotencyKey: idemKey,
	}
	txErr := e.withTenant(ctx, func(tx pgx.Tx) error {
		providerMsgID, serr := ms.SendMessage(ctx, out)
		if serr != nil {
			return serr
		}
		// Grava o TEXTO no histórico (dashboard legível); idempotente.
		return e.deps.Messages.RecordOutbound(ctx, tx, idemKey, inbound.TenantID, conv.ID, providerMsgID, text, reasoningJSON)
	})
	if txErr != nil {
		log.Error("tts: enviar/gravar", "err", txErr)
		return false
	}
	if e.deps.Broadcast != nil {
		e.deps.Broadcast(WSEvent{Type: "message.outbound", ConversationID: conv.ID})
	}
	return true
}
```

> Nota: confirme o helper de logger usado no engine (ex.: `e.logger()` ou `e.deps.Logger`); use o mesmo padrão já existente no arquivo. Se for `log` local já definido no início de `Handle`, reaproveite-o passando-o como parâmetro em vez de `e.logger()`.

- [ ] **Step 5: Passar o Voice nas duas `EngineDeps` do `main.go`** — no engine Meta (`engine := NewConversationEngine(EngineDeps{...})`, ~93) adicionar `Voice: voiceClient,`. (No `evoEngine` é inofensivo, mas como é Evolution o `trySendVoice` retorna false por type-assert; pode deixar sem.)

- [ ] **Step 6:** `go build ./... && go vet ./... && go test ./...` → tudo verde. Se o `goto`/label gerar erro de "jumps over declaration", mover declarações para antes do `if` ou trocar o `goto` por um `if/else` envolvendo o loop (preferir if/else se mais limpo).

- [ ] **Step 7: Commit** `git add engine.go main.go engine_voice_test.go && git commit -m "feat(bot): responder em nota de voz quando o cliente manda áudio (espelho)"`

---

## FASE C — Imagem, modelos e deploy

### Task C1: Dockerfile com ffmpeg + whisper.cpp + piper e entrypoint dos modelos

**Files:** `Dockerfile`, Create `entrypoint.sh`

- [ ] **Step 1: Reescrever `Dockerfile`.** O runtime sai de `distroless/static` (sem shell/libs) para `debian:bookworm-slim` com `ffmpeg`, `whisper.cpp` e `piper`. whisper.cpp é compilado no builder; piper baixado (release). Conteúdo:

```dockerfile
# ---- build do bot (Go) ----
FROM golang:1.24-bookworm AS gobuild
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o bot .

# ---- build do whisper.cpp ----
FROM debian:bookworm-slim AS whisperbuild
RUN apt-get update && apt-get install -y --no-install-recommends \
      git build-essential cmake ca-certificates && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 https://github.com/ggerganov/whisper.cpp /w \
 && cd /w && cmake -B build && cmake --build build --config Release -j \
 && cp build/bin/whisper-cli /usr/local/bin/whisper-cli

# ---- runtime ----
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      ffmpeg ca-certificates curl && rm -rf /var/lib/apt/lists/*
# piper (binário pré-compilado)
RUN curl -fsSL -o /tmp/piper.tar.gz \
      https://github.com/rhasspy/piper/releases/download/2023.11.14-2/piper_linux_x86_64.tar.gz \
 && tar -xzf /tmp/piper.tar.gz -C /usr/local/lib \
 && ln -s /usr/local/lib/piper/piper /usr/local/bin/piper \
 && rm /tmp/piper.tar.gz
COPY --from=whisperbuild /usr/local/bin/whisper-cli /usr/local/bin/whisper-cli
COPY --from=gobuild /app/bot /bot
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 3000
ENTRYPOINT ["/entrypoint.sh"]
```

- [ ] **Step 2: Criar `entrypoint.sh`** — baixa os modelos no volume `/models` se ausentes, depois sobe o bot:

```bash
#!/usr/bin/env bash
set -euo pipefail
MODELS=/models
mkdir -p "$MODELS"

WHISPER="${WHISPER_MODEL:-$MODELS/ggml-small-q5_1.bin}"
PIPER_ONNX="${PIPER_VOICE:-$MODELS/pt_BR-faber-medium.onnx}"

if [ "${VOICE_ENABLED:-false}" = "true" ]; then
  if [ ! -f "$WHISPER" ]; then
    echo "baixando modelo whisper…"
    curl -fsSL -o "$WHISPER" \
      https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small-q5_1.bin
  fi
  if [ ! -f "$PIPER_ONNX" ]; then
    echo "baixando voz piper pt-BR…"
    curl -fsSL -o "$PIPER_ONNX" \
      https://huggingface.co/rhasspy/piper-voices/resolve/main/pt/pt_BR/faber/medium/pt_BR-faber-medium.onnx
    curl -fsSL -o "$PIPER_ONNX.json" \
      https://huggingface.co/rhasspy/piper-voices/resolve/main/pt/pt_BR/faber/medium/pt_BR-faber-medium.onnx.json
  fi
fi

exec /bot
```

- [ ] **Step 3: Build local da imagem (sanity)** — confirma que a imagem compila e os binários existem:

```bash
cd /home/guilherme/projetos/sg/santos-tech-infra/apps/bot-go
docker build -t bot-voice-test . && \
docker run --rm --entrypoint sh bot-voice-test -c "which ffmpeg whisper-cli piper && /bot --help 2>/dev/null; echo OK"
```
Expected: caminhos dos 3 binários impressos + "OK". (Se `docker` não estiver disponível na máquina, registrar como verificação a fazer no servidor.)

- [ ] **Step 4: Commit** `git add Dockerfile entrypoint.sh && git commit -m "build(bot): imagem com ffmpeg+whisper.cpp+piper e entrypoint dos modelos"`

---

### Task C2: .env.example, docs e notas de deploy

**Files:** `.env.example` (se existir no bot-go), `docs`/`llms.txt` central

- [ ] **Step 1: Documentar as envs** no `.env.example` do bot-go (se existir; senão pular):

```
# Voz (STT/TTS local) — opcional
VOICE_ENABLED=false
WHISPER_MODEL=/models/ggml-small-q5_1.bin
PIPER_VOICE=/models/pt_BR-faber-medium.onnx
```

- [ ] **Step 2: llms.txt central** — registrar que o bot (Meta) agora aceita voz (transcreve) e responde em voz no modo espelho, quando `VOICE_ENABLED=true`. (Se a doc não detalhar o bot, pular — não inventar formato.)

- [ ] **Step 3: Notas de deploy (Easypanel)** — anotar no PR: criar **volume persistente** montado em `/models`; garantir **RAM ≥2GB** no serviço; setar `VOICE_ENABLED=true`. Primeiro boot baixa os modelos (~245MB) — pode demorar.

- [ ] **Step 4: Verificação manual no servidor (anotar resultado):**
  - Mandar um áudio real pro número Meta → conferir que o bot respondeu coerente (STT ok) **em nota de voz** (TTS ok).
  - Medir latência aproximada.
  - `VOICE_ENABLED=false` → volta a responder em texto (fallback).
  - Forçar erro (ex.: modelo ausente) → resposta sai em texto, sem travar.

- [ ] **Step 5: Commit** (se houve mudança de docs) `git commit -am "docs(bot): voz no WhatsApp (espelho) — envs e deploy"`

- [ ] **Step 6: PR**

```bash
git push -u origin feat/whats-voz-audio
gh pr create --base master --title "feat(bot): responder em áudio (espelho) — STT+TTS local" --body "..."
```

---

## Cobertura do spec (self-review)

- **STT (entender voz):** A2 (Transcribe), A3 (download mídia), A4/A5 (WasVoice + hook na ingestão). ✅
- **TTS (responder voz, espelho):** B1 (Synthesize/join), B2 (upload), B3 (audio por id), B4 (decisão + envio + fallback). ✅
- **Local/binários/modelos/volume/RAM:** C1 (imagem + entrypoint), C2 (deploy). ✅
- **Config `VOICE_ENABLED` + fallback texto:** A1 (config), A5/B4 (best-effort em todos os passos). ✅
- **Limpeza de temporários:** A2/B1 (`defer os.RemoveAll`). ✅
- **Uma nota de voz por resposta:** B1 `joinBubbles` + B4 envio único. ✅
- **Não guardar o áudio:** B4 grava só o texto em `RecordOutbound`. ✅
- **Testes:** unitários puros (args, join, decisão), httptest (download/upload/contentToBody); execução real = manual (C2). ✅
