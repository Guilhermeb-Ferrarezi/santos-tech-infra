package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"time"
)

// voiceHTTPClient é o http.Client das chamadas de voz (OpenAI). Bounded (dial/header/
// idle timeouts) e **forçando HTTP/1.1**: dentro do container, o HTTP/2 pra OpenAI
// (atrás do Cloudflare) pendurava esperando os headers ("http2: timeout awaiting
// response headers") — stall de h2 por MTU/PMTUD. h1.1 não tem esse modo de falha.
func voiceHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 8 * time.Second}).DialContext,
			ResponseHeaderTimeout: 20 * time.Second,
			IdleConnTimeout:       30 * time.Second,
			ForceAttemptHTTP2:     false,
			// Mapa não-nulo desabilita o upgrade automático pra HTTP/2 → usa HTTP/1.1.
			TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
		},
	}
}

// VoiceSelection — provedor e voz escolhidos no painel para um tenant.
// Campos vazios caem no default do ambiente.
type VoiceSelection struct {
	Provider string // "openai" | "elevenlabs" (vazio = openai)
	VoiceID  string
	Model    string
}

// VoiceClient faz STT (sempre OpenAI) e TTS (OpenAI ou ElevenLabs). Sem binários
// locais — tudo por HTTP.
type VoiceClient struct {
	enabled  bool
	apiKey   string
	baseURL  string
	ttsVoice string
	ttsModel string
	sttModel string

	elevenKey     string
	elevenBaseURL string
	elevenModel   string

	http *http.Client
}

func NewVoiceClient(cfg Config) *VoiceClient {
	return &VoiceClient{
		enabled:  cfg.VoiceEnabled && cfg.OpenAIKey != "",
		apiKey:   cfg.OpenAIKey,
		baseURL:  cfg.OpenAIBaseURL,
		ttsVoice: cfg.OpenAITTSVoice,
		ttsModel: cfg.OpenAITTSModel,
		sttModel: cfg.OpenAISTTModel,

		elevenKey:     cfg.ElevenLabsKey,
		elevenBaseURL: cfg.ElevenLabsBaseURL,
		elevenModel:   cfg.ElevenLabsModel,

		http: voiceHTTPClient(),
	}
}

// resolve decide o provedor efetivo. ElevenLabs só entra se houver chave E
// voz escolhida — sem isso cai no OpenAI, que é o caminho que sempre funciona.
func (v *VoiceClient) resolve(sel VoiceSelection) VoiceSelection {
	if sel.Provider == "elevenlabs" && v.elevenKey != "" && sel.VoiceID != "" {
		if sel.Model == "" {
			sel.Model = v.elevenModel
		}
		return sel
	}
	out := VoiceSelection{Provider: "openai", VoiceID: sel.VoiceID, Model: sel.Model}
	if sel.Provider == "elevenlabs" {
		// Escolha inválida (sem chave ou sem voz): ignora a voz, que é de outro
		// provedor, e usa o default do ambiente.
		out.VoiceID, out.Model = "", ""
	}
	if out.VoiceID == "" {
		out.VoiceID = v.ttsVoice
	}
	if out.Model == "" {
		out.Model = v.ttsModel
	}
	return out
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
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
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

// joinBubbles junta os balões num texto único (uma nota de voz por resposta).
func joinBubbles(bubbles []string) string {
	return strings.Join(bubbles, "\n")
}

// Synthesize gera a nota de voz (OGG/Opus) com a voz escolhida para o tenant.
// Os dois provedores devolvem OGG/Opus, que é o que o WhatsApp aceita como
// nota de voz — nenhum transcode local é necessário.
func (v *VoiceClient) Synthesize(ctx context.Context, text string, sel VoiceSelection) ([]byte, error) {
	s := v.resolve(sel)
	if s.Provider == "elevenlabs" {
		return v.synthesizeElevenLabs(ctx, text, s)
	}
	return v.synthesizeOpenAI(ctx, text, s)
}

func (v *VoiceClient) synthesizeOpenAI(ctx context.Context, text string, sel VoiceSelection) ([]byte, error) {
	body, err := json.Marshal(map[string]any{
		"model":           sel.Model,
		"voice":           sel.VoiceID,
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
	return v.doTTS(req, "openai")
}

// synthesizeElevenLabs usa output_format=opus_48000_64 — devolve OGG/Opus direto.
//
// Atenção operacional: vozes de **clonagem instantânea** (categoria "cloned")
// exigem plano pago com IVC habilitado. Num plano sem IVC a API responde 401
// `subscription_required` e o chamador cai no texto. Vozes de **Voice Design**
// (categoria "generated") não têm essa restrição.
func (v *VoiceClient) synthesizeElevenLabs(ctx context.Context, text string, sel VoiceSelection) ([]byte, error) {
	body, err := json.Marshal(map[string]any{
		"text":     text,
		"model_id": sel.Model,
	})
	if err != nil {
		return nil, fmt.Errorf("voice: 11l marshal: %w", err)
	}
	url := fmt.Sprintf("%s/text-to-speech/%s?output_format=opus_48000_64", v.elevenBaseURL, sel.VoiceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", v.elevenKey)
	req.Header.Set("Content-Type", "application/json")
	return v.doTTS(req, "elevenlabs")
}

// doTTS executa a requisição e valida a resposta. `provider` só entra na mensagem
// de erro, para o log dizer qual lado falhou.
func (v *VoiceClient) doTTS(req *http.Request, provider string) ([]byte, error) {
	resp, err := v.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voice: tts do (%s): %w", provider, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("voice: tts status %d (%s): %s", resp.StatusCode, provider, string(data))
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("voice: tts (%s): resposta vazia", provider)
	}
	return data, nil
}
