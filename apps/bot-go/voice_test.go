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
