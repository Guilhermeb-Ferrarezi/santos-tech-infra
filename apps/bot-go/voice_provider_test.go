package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// resolve é a regra que decide o provedor efetivo. O caso que mais importa é o
// fail-safe: escolha de ElevenLabs incompleta NÃO pode virar chamada quebrada —
// tem que cair no OpenAI, que é o caminho que sempre funciona.
func TestResolveVoiceSelection(t *testing.T) {
	v := &VoiceClient{
		ttsVoice: "nova", ttsModel: "gpt-4o-mini-tts",
		elevenKey: "11k", elevenModel: "eleven_multilingual_v2",
	}
	semChave := &VoiceClient{
		ttsVoice: "nova", ttsModel: "gpt-4o-mini-tts",
		elevenModel: "eleven_multilingual_v2",
	}

	casos := []struct {
		nome      string
		client    *VoiceClient
		in        VoiceSelection
		provider  string
		voiceID   string
		modelo    string
		descricao string
	}{
		{"vazio cai no openai do ambiente", v, VoiceSelection{},
			"openai", "nova", "gpt-4o-mini-tts", "sem nada escolhido"},
		{"openai com voz propria", v, VoiceSelection{Provider: "openai", VoiceID: "shimmer"},
			"openai", "shimmer", "gpt-4o-mini-tts", "voz escolhida, modelo default"},
		{"elevenlabs completo", v, VoiceSelection{Provider: "elevenlabs", VoiceID: "abc123"},
			"elevenlabs", "abc123", "eleven_multilingual_v2", "modelo cai no default do 11l"},
		{"elevenlabs sem voz cai no openai", v, VoiceSelection{Provider: "elevenlabs"},
			"openai", "nova", "gpt-4o-mini-tts", "sem voice_id não dá pra chamar o 11l"},
		{"elevenlabs sem chave cai no openai", semChave, VoiceSelection{Provider: "elevenlabs", VoiceID: "abc123"},
			"openai", "nova", "gpt-4o-mini-tts", "sem ELEVENLABS_API_KEY"},
		{"provider desconhecido cai no openai", v, VoiceSelection{Provider: "azure", VoiceID: "x"},
			"openai", "x", "gpt-4o-mini-tts", "provider não implementado"},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got := c.client.resolve(c.in)
			if got.Provider != c.provider {
				t.Errorf("provider = %q, queria %q (%s)", got.Provider, c.provider, c.descricao)
			}
			if got.VoiceID != c.voiceID {
				t.Errorf("voiceID = %q, queria %q", got.VoiceID, c.voiceID)
			}
			if got.Model != c.modelo {
				t.Errorf("model = %q, queria %q", got.Model, c.modelo)
			}
		})
	}
}

// Quando o tenant escolhe ElevenLabs, a chamada tem que ir para o endpoint certo,
// com o header certo e pedindo OGG/Opus — que é o que o WhatsApp aceita.
func TestSynthesizeElevenLabs(t *testing.T) {
	var gotPath, gotKey, gotFormat string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("xi-api-key")
		gotFormat = r.URL.Query().Get("output_format")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte("OggS-FAKE"))
	}))
	defer srv.Close()

	v := &VoiceClient{
		enabled: true, apiKey: "k", baseURL: srv.URL,
		ttsVoice: "nova", ttsModel: "gpt-4o-mini-tts",
		elevenKey: "11k", elevenBaseURL: srv.URL, elevenModel: "eleven_multilingual_v2",
		http: &http.Client{},
	}

	got, err := v.Synthesize(context.Background(), "olá",
		VoiceSelection{Provider: "elevenlabs", VoiceID: "VOZ123"})
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if string(got) != "OggS-FAKE" {
		t.Fatalf("áudio = %q", got)
	}
	if !strings.HasSuffix(gotPath, "/text-to-speech/VOZ123") {
		t.Errorf("path = %q", gotPath)
	}
	if gotKey != "11k" {
		t.Errorf("xi-api-key = %q", gotKey)
	}
	if !strings.HasPrefix(gotFormat, "opus_") {
		t.Errorf("output_format = %q — o WhatsApp precisa de OGG/Opus", gotFormat)
	}
	if gotBody["text"] != "olá" {
		t.Errorf("text = %v", gotBody["text"])
	}
	if gotBody["model_id"] != "eleven_multilingual_v2" {
		t.Errorf("model_id = %v", gotBody["model_id"])
	}
}

// Erro do provedor tem que virar erro do Synthesize — o engine depende disso
// para cair no texto em vez de mandar um áudio vazio.
func TestSynthesizeErroViraErro(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":{"code":"subscription_required"}}`))
	}))
	defer srv.Close()

	v := &VoiceClient{
		enabled: true, apiKey: "k", baseURL: srv.URL,
		elevenKey: "11k", elevenBaseURL: srv.URL, elevenModel: "m",
		http: &http.Client{},
	}
	_, err := v.Synthesize(context.Background(), "oi",
		VoiceSelection{Provider: "elevenlabs", VoiceID: "clonada"})
	if err == nil {
		t.Fatal("401 deveria virar erro")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "elevenlabs") {
		t.Errorf("erro pouco informativo: %v", err)
	}
}

// Resposta 200 mas vazia também é falha — sem isso o bot enviaria um áudio
// de zero byte e o cliente receberia uma nota de voz quebrada.
func TestSynthesizeVazioViraErro(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	v := &VoiceClient{
		enabled: true, apiKey: "k", baseURL: srv.URL,
		ttsVoice: "nova", ttsModel: "m", http: &http.Client{},
	}
	if _, err := v.Synthesize(context.Background(), "oi", VoiceSelection{}); err == nil {
		t.Fatal("corpo vazio deveria virar erro")
	}
}
