# Bot responde em áudio (espelho) via OpenAI — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Quando o cliente manda voz no WhatsApp (Meta), o bot transcreve (OpenAI STT), responde com a IA atual e devolve a resposta como nota de voz feminina (OpenAI TTS). Sem binários/infra local.

**Architecture:** Um `VoiceClient` (voice.go) chama a OpenAI por HTTP: `/v1/audio/transcriptions` (STT) e `/v1/audio/speech` (TTS, `response_format=opus`). STT acontece na ingestão do webhook (preenche `Transcript` + `WasVoice`); TTS no envio quando o inbound foi voz (espelho), com **fallback para texto** em qualquer falha. **Dockerfile não muda** (segue distroless; só HTTP em Go).

**Tech Stack:** Go 1.24, OpenAI Audio API, Meta Cloud API (Graph v21.0).

**Caminho:** `/home/guilherme/projetos/sg/santos-tech-infra/apps/bot-go`. `export PATH="$HOME/.local/bin:$PATH"` p/ ter `go`. Branch: `feat/whats-voz-audio` (já criada).

**Testes:** sem DB; tudo testável por **httptest** (OpenAI e Meta mockados via `OPENAI_BASE_URL`/URL injetada) + funções puras. Execução com key real = verificação manual (Task C1).

---

## FASE A — STT (entender a voz do cliente)

### Task A1: Config de voz (OpenAI)

**Files:** `config.go`

- [ ] **Step 1: Campos no struct `Config`** (após o bloco "Dashboard"):

```go
	// Voz (STT/TTS via OpenAI). VOICE_ENABLED liga a feature.
	VoiceEnabled   bool
	OpenAIKey      string
	OpenAIBaseURL  string
	OpenAITTSVoice string
	OpenAITTSModel string
	OpenAISTTModel string
```

- [ ] **Step 2: Preencher em `LoadConfig`** (antes do fechamento do `return Config{...}`):

```go
		VoiceEnabled:   getEnv("VOICE_ENABLED", "false") == "true",
		OpenAIKey:      getEnv("OPENAI_API_KEY", ""),
		OpenAIBaseURL:  strings.TrimRight(getEnv("OPENAI_BASE_URL", "https://api.openai.com/v1"), "/"),
		OpenAITTSVoice: getEnv("OPENAI_TTS_VOICE", "nova"),
		OpenAITTSModel: getEnv("OPENAI_TTS_MODEL", "gpt-4o-mini-tts"),
		OpenAISTTModel: getEnv("OPENAI_STT_MODEL", "whisper-1"),
```

- [ ] **Step 3:** `go build ./...` → sem erro.
- [ ] **Step 4: Commit** `git add config.go && git commit -m "feat(bot): config de voz (OpenAI STT/TTS)"`

---

### Task A2: VoiceClient + STT (Transcribe) via OpenAI

**Files:** Create `voice.go`, Test `voice_test.go`

- [ ] **Step 1: Teste httptest do STT** (`voice_test.go`):

```go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestVoice(baseURL string) *VoiceClient {
	return &VoiceClient{
		enabled: true, apiKey: "k", baseURL: baseURL,
		ttsVoice: "nova", ttsModel: "gpt-4o-mini-tts", sttModel: "whisper-1",
		http: &http.Client{},
	}
}

func TestTranscribe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Errorf("path inesperado: %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("content-type: %s", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"olá tudo bem"}`))
	}))
	defer srv.Close()

	got, err := newTestVoice(srv.URL).Transcribe(context.Background(), []byte("OGG"), "audio/ogg")
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if got != "olá tudo bem" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2:** `go test ./... -run TestTranscribe` → FAIL (undefined).

- [ ] **Step 3: Implementar `voice.go`:**

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// VoiceClient faz STT e TTS via OpenAI (HTTP). Sem binários locais.
type VoiceClient struct {
	enabled  bool
	apiKey   string
	baseURL  string
	ttsVoice string
	ttsModel string
	sttModel string
	http     *http.Client
}

func NewVoiceClient(cfg Config) *VoiceClient {
	return &VoiceClient{
		enabled:  cfg.VoiceEnabled && cfg.OpenAIKey != "",
		apiKey:   cfg.OpenAIKey,
		baseURL:  cfg.OpenAIBaseURL,
		ttsVoice: cfg.OpenAITTSVoice,
		ttsModel: cfg.OpenAITTSModel,
		sttModel: cfg.OpenAISTTModel,
		http:     &http.Client{Timeout: 60 * time.Second},
	}
}

func (v *VoiceClient) Enabled() bool { return v != nil && v.enabled }

// Transcribe envia o áudio (OGG/Opus do WhatsApp) ao STT da OpenAI e devolve o texto PT.
func (v *VoiceClient) Transcribe(ctx context.Context, audio []byte, mime string) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "audio.ogg")
	if err != nil {
		return "", fmt.Errorf("voice: stt form: %w", err)
	}
	if _, err := fw.Write(audio); err != nil {
		return "", fmt.Errorf("voice: stt write: %w", err)
	}
	_ = mw.WriteField("model", v.sttModel)
	_ = mw.WriteField("language", "pt")
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("voice: stt close: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.baseURL+"/audio/transcriptions", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+v.apiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := v.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("voice: stt do: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("voice: stt status %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("voice: stt decode: %w", err)
	}
	return out.Text, nil
}
```

- [ ] **Step 4:** `go test ./... -run TestTranscribe` → PASS.
- [ ] **Step 5:** `go vet ./... && gofmt -l voice.go voice_test.go` (limpo).
- [ ] **Step 6: Commit** `git add voice.go voice_test.go && git commit -m "feat(bot): VoiceClient STT via OpenAI"`

---

### Task A3: Download de mídia do Meta (WhatsAppSender.DownloadMedia)

**Files:** `whatsapp.go`, Test `whatsapp_media_test.go`

- [ ] **Step 1: Teste httptest** (`whatsapp_media_test.go`):

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
			_, _ = w.Write([]byte(`{"url":"http://` + r.Host + `/bytes","mime_type":"audio/ogg"}`))
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

- [ ] **Step 2:** `go test ./... -run TestDownloadMedia` → FAIL.

- [ ] **Step 3: Implementar em `whatsapp.go`** (após `post`):

```go
// DownloadMedia baixa o conteúdo de uma mídia do WhatsApp pelo media id (lookup→bytes).
func (s *WhatsAppSender) DownloadMedia(ctx context.Context, mediaID string) ([]byte, string, error) {
	return s.downloadMediaFrom(ctx, fmt.Sprintf("https://graph.facebook.com/v21.0/%s", mediaID))
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
	data, err := io.ReadAll(io.LimitReader(dresp.Body, 16<<20))
	if err != nil {
		return nil, "", fmt.Errorf("whatsapp: media read: %w", err)
	}
	return data, meta.MimeType, nil
}
```

- [ ] **Step 4:** `go test ./... -run TestDownloadMedia` → PASS.
- [ ] **Step 5: Commit** `git add whatsapp.go whatsapp_media_test.go && git commit -m "feat(bot): download de mídia do Meta (para STT)"`

---

### Task A4: Campo `WasVoice` + preservar na combinação da rajada

**Files:** `types.go`, `server.go` (combineInbound)

- [ ] **Step 1: `WasVoice` em `InboundMessage`** (`types.go`, após `ReceivedAt`):

```go
	ReceivedAt        time.Time
	// WasVoice: a mensagem original do cliente era nota de voz (modo espelho).
	WasVoice bool
```

- [ ] **Step 2: Preservar em `combineInbound`** (`server.go`), após `base := items[len(items)-1].msg`:

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
- [ ] **Step 4: Commit** `git add types.go server.go && git commit -m "feat(bot): InboundMessage.WasVoice"`

---

### Task A5: Wire do Voice no Server + hook de STT na ingestão

**Files:** `server.go`, `main.go`

- [ ] **Step 1: Campo `voice` no `Server`** (junto de `evoClient`):

```go
	evoClient  *EvolutionClient
	voice      *VoiceClient
```

- [ ] **Step 2: `NewServer`** — adicionar `voice *VoiceClient` ao FINAL da assinatura; no struct literal, após `evoClient:  evoClient,` adicionar `voice:      voice,`.

- [ ] **Step 3: Hook de STT em `processInbound`** — logo após `for _, msg := range msgs {`, ANTES de `msg.TenantID = ...`:

```go
	for _, msg := range msgs {
		// STT: transcreve nota de voz antes de tudo (Transcript + WasVoice). Best-effort.
		if msg.Content.Type == "audio" && msg.Content.Transcript == nil &&
			s.voice.Enabled() && s.sender != nil && msg.Content.MediaURL != nil {
			mediaID := strings.TrimPrefix(*msg.Content.MediaURL, "wa_media_id:")
			if audio, mime, derr := s.sender.DownloadMedia(ctx, mediaID); derr != nil {
				s.logger.Error("stt: download mídia", "err", derr)
			} else if text, terr := s.voice.Transcribe(ctx, audio, mime); terr != nil {
				s.logger.Error("stt: transcrição", "err", terr)
			} else if text != "" {
				msg.Content.Transcript = &text
				msg.WasVoice = true
			}
		}

		msg.TenantID = s.cfg.TenantID
```

(`strings` já está importado em server.go.)

- [ ] **Step 4: `main.go`** — após `sender := NewWhatsAppSender(...)`:

```go
	voiceClient := NewVoiceClient(cfg)
```

E passar `voiceClient` como ÚLTIMO argumento na chamada `NewServer(...)`.

- [ ] **Step 5:** `go build ./... && go vet ./... && go test ./...` → verde.
- [ ] **Step 6: Commit** `git add server.go main.go && git commit -m "feat(bot): STT na ingestão (transcreve voz do cliente)"`

---

## FASE B — TTS (responder em nota de voz feminina)

### Task B1: VoiceClient.Synthesize (OpenAI TTS) + joinBubbles

**Files:** `voice.go`, `voice_test.go`

- [ ] **Step 1: Testes** (adicionar em `voice_test.go`):

```go
func TestJoinBubbles(t *testing.T) {
	if got := joinBubbles([]string{"Oi!", "Tudo bem?"}); got != "Oi!\nTudo bem?" {
		t.Fatalf("joinBubbles = %q", got)
	}
}

func TestSynthesize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/speech" {
			t.Errorf("path: %s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["voice"] != "nova" || body["response_format"] != "opus" {
			t.Errorf("body inesperado: %v", body)
		}
		_, _ = w.Write([]byte("OGGAUDIO"))
	}))
	defer srv.Close()

	got, err := newTestVoice(srv.URL).Synthesize(context.Background(), "olá")
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if string(got) != "OGGAUDIO" {
		t.Fatalf("got %q", got)
	}
}
```

(Adicionar `"encoding/json"` aos imports do `voice_test.go`.)

- [ ] **Step 2:** `go test ./... -run 'TestJoinBubbles|TestSynthesize'` → FAIL.

- [ ] **Step 3: Implementar em `voice.go`:**

```go
import "strings" // adicionar ao import block existente

// joinBubbles junta os balões num texto único (uma nota de voz por resposta).
func joinBubbles(bubbles []string) string {
	return strings.Join(bubbles, "\n")
}

// Synthesize gera a nota de voz (OGG/Opus) com a voz feminina configurada.
func (v *VoiceClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	body, err := json.Marshal(map[string]any{
		"model":           v.ttsModel,
		"voice":           v.ttsVoice,
		"input":           text,
		"response_format": "opus",
	})
	if err != nil {
		return nil, fmt.Errorf("voice: tts marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.baseURL+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+v.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voice: tts do: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("voice: tts status %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}
```

- [ ] **Step 4:** `go test ./... -run 'TestJoinBubbles|TestSynthesize'` → PASS.
- [ ] **Step 5: Commit** `git add voice.go voice_test.go && git commit -m "feat(bot): VoiceClient TTS via OpenAI (voz feminina)"`

---

### Task B2: Upload de áudio no Meta (WhatsAppSender.UploadAudio)

**Files:** `whatsapp.go`, Test `whatsapp_media_test.go`

- [ ] **Step 1: Teste** (adicionar em `whatsapp_media_test.go`):

```go
func TestUploadAudio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

- [ ] **Step 3: Implementar em `whatsapp.go`** (precisa `mime/multipart` no import):

```go
// UploadAudio envia bytes OGG/Opus ao endpoint de mídia do Meta e devolve o media id.
func (s *WhatsAppSender) UploadAudio(ctx context.Context, ogg []byte) (string, error) {
	return s.uploadAudioTo(ctx, fmt.Sprintf("https://graph.facebook.com/v21.0/%s/media", s.phoneNumberID), ogg)
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

- [ ] **Step 1: Teste:**

```go
func TestContentToBodyAudioByID(t *testing.T) {
	id := "wa_media_id:MEDIA123"
	audio := contentToBody(MessageContent{Type: "audio", MediaURL: &id})["audio"].(map[string]any)
	if audio["id"] != "MEDIA123" {
		t.Fatalf("esperava id, got %v", audio)
	}
	if _, has := audio["link"]; has {
		t.Fatalf("não deveria ter link")
	}
	link := "https://x/a.ogg"
	a2 := contentToBody(MessageContent{Type: "audio", MediaURL: &link})["audio"].(map[string]any)
	if a2["link"] != "https://x/a.ogg" {
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

- [ ] **Step 1: `Voice` no `EngineDeps`** (`engine.go`, após `Notion *NotionClient`):

```go
	// Voice — STT/TTS via OpenAI (opcional). Habilitado → responde em áudio às mensagens de voz.
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
		t.Fatal("voz+on deveria ser true")
	}
	if shouldReplyAsAudio(on, InboundMessage{WasVoice: false}) {
		t.Fatal("texto não vira áudio")
	}
	if shouldReplyAsAudio(off, InboundMessage{WasVoice: true}) {
		t.Fatal("off deveria ser false")
	}
	if shouldReplyAsAudio(nil, InboundMessage{WasVoice: true}) {
		t.Fatal("nil deveria ser false")
	}
}
```

- [ ] **Step 3:** `go test ./... -run TestShouldReplyAsAudio` → FAIL.

- [ ] **Step 4: Implementar no `engine.go`** o helper + o envio. Helper:

```go
// shouldReplyAsAudio: responde em áudio só quando o cliente mandou voz e o TTS está ligado.
func shouldReplyAsAudio(v *VoiceClient, inbound InboundMessage) bool {
	return inbound.WasVoice && v.Enabled()
}
```

No bloco de envio, ANTES do `for i, bubble := range output.Bubbles {`, tentar a voz e
**envolver o loop de texto num `if !sentVoice`** (sem `goto`):

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
			// ... corpo atual do loop, INALTERADO (indentado dentro do if) ...
		}
	}
```

Indente o `for` existente dentro do `if !sentVoice {`. O código após o loop (broadcast/fase 3)
continua igual, FORA do `if`. Implementar `trySendVoice` perto do fim do arquivo:

```go
// trySendVoice gera UMA nota de voz com a resposta inteira e envia pelo Meta.
// Retorna false (com log) em qualquer falha → chamador cai no texto.
func (e *ConversationEngine) trySendVoice(ctx context.Context, conv Conversation, inbound InboundMessage, output ResponderOutput, reasoningJSON *string) bool {
	log := e.deps.Logger
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

> Confirme como o engine referencia o logger (no início de `Handle` há um `log := ...`). Use o
> mesmo: se for `log := e.deps.Logger` no topo de `Handle`, em `trySendVoice` use `e.deps.Logger`
> (como acima). Ajuste se o nome do campo divergir.

- [ ] **Step 5: `main.go`** — na `EngineDeps{...}` do engine Meta (`engine := NewConversationEngine(...)`), adicionar `Voice: voiceClient,`.

- [ ] **Step 6:** `go build ./... && go vet ./... && go test ./...` → tudo verde.
- [ ] **Step 7: Commit** `git add engine.go main.go engine_voice_test.go && git commit -m "feat(bot): responde em nota de voz quando o cliente manda áudio (espelho)"`

---

## FASE C — Docs e verificação

### Task C1: .env.example, docs e PR

- [ ] **Step 1: `.env.example`** do bot-go (se existir; senão pular):

```
# Voz (STT/TTS via OpenAI) — opcional
VOICE_ENABLED=false
OPENAI_API_KEY=
OPENAI_TTS_VOICE=nova
```

- [ ] **Step 2: llms.txt central** — registrar que o bot (Meta) aceita voz (transcreve) e responde
  em voz feminina no modo espelho quando `VOICE_ENABLED=true`. (Se não detalhar o bot, pular.)

- [ ] **Step 3: Deploy (anotar no PR):** sem mudança de imagem. Setar no Easypanel
  `VOICE_ENABLED=true` + `OPENAI_API_KEY` (+ opcional `OPENAI_TTS_VOICE`).

- [ ] **Step 4: Verificação manual** (com key real): mandar áudio real → conferir transcrição +
  resposta em voz feminina; `VOICE_ENABLED=0` → volta a texto; key inválida → texto (fallback).

- [ ] **Step 5: Commit + PR**

```bash
git commit -am "docs(bot): voz no WhatsApp (espelho via OpenAI) — envs e deploy"
git push -u origin feat/whats-voz-audio
gh pr create --base master --title "feat(bot): responder em áudio (espelho) via OpenAI" --body "..."
```

---

## Cobertura do spec (self-review)
- STT: A2 (Transcribe OpenAI), A3 (download mídia), A4/A5 (WasVoice + hook). ✅
- TTS feminino: B1 (Synthesize OpenAI, voz nova), B2 (upload), B3 (audio por id), B4 (decisão+envio+fallback). ✅
- Config OpenAI + `VOICE_ENABLED` + base URL injetável p/ testes: A1. ✅
- Sem infra local / Dockerfile inalterado: refletido (sem Fase de Docker). ✅
- Uma nota de voz, não guardar áudio, idempotência: B1/B4. ✅
- Testes httptest + puros: A2/A3/B1/B2/B3/B4. ✅
