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
		http:     voiceHTTPClient(),
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
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("voice: tts status %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}
