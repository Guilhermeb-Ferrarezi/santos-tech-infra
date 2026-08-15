package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestBuildOpSubmitDeepgramURL(t *testing.T) {
	req, err := buildOpSubmit(opAdapterDeepgram, opTranscribe, apiRouterOpInput{URL: "https://cdn.example.com/a.mp3"})
	if err != nil {
		t.Fatalf("buildOpSubmit: %v", err)
	}
	if req.Path != "/v1/listen?model=nova-3&smart_format=true" {
		t.Errorf("path=%s", req.Path)
	}
	if req.ContentType != "application/json" {
		t.Errorf("contentType=%s", req.ContentType)
	}
	if !strings.Contains(string(req.Body), "https://cdn.example.com/a.mp3") {
		t.Errorf("body=%s", req.Body)
	}
}

func TestBuildOpSubmitDeepgramAudioB64(t *testing.T) {
	req, err := buildOpSubmit(opAdapterDeepgram, opTranscribe, apiRouterOpInput{AudioB64: base64.StdEncoding.EncodeToString([]byte("fake-audio")), AudioMime: "audio/mpeg"})
	if err != nil {
		t.Fatalf("buildOpSubmit: %v", err)
	}
	if req.ContentType != "audio/mpeg" {
		t.Errorf("contentType=%s, queria audio/mpeg", req.ContentType)
	}
	if string(req.Body) != "fake-audio" {
		t.Errorf("body decodificado errado: %q", req.Body)
	}
}

func TestBuildOpSubmitDeepgramSemAudio(t *testing.T) {
	if _, err := buildOpSubmit(opAdapterDeepgram, opTranscribe, apiRouterOpInput{}); err == nil {
		t.Fatalf("esperava erro com url e audioB64 vazios")
	}
}

func TestBuildOpSubmitAssemblyAI(t *testing.T) {
	req, err := buildOpSubmit(opAdapterAssemblyAI, opTranscribe, apiRouterOpInput{URL: "https://cdn.example.com/a.wav"})
	if err != nil {
		t.Fatalf("buildOpSubmit: %v", err)
	}
	if req.Path != "/v2/transcript" {
		t.Errorf("path=%s", req.Path)
	}
	if !strings.Contains(string(req.Body), "https://cdn.example.com/a.wav") {
		t.Errorf("body=%s", req.Body)
	}
	if !opAsync(opAdapterAssemblyAI, opTranscribe) {
		t.Error("assemblyai transcribe deveria ser assíncrono")
	}
}

func TestBuildOpSubmitElevenLabsTTS(t *testing.T) {
	req, err := buildOpSubmit(opAdapterElevenLabs, opTTS, apiRouterOpInput{Text: "olá mundo", Voice: "v123"})
	if err != nil {
		t.Fatalf("buildOpSubmit: %v", err)
	}
	if req.Path != "/v1/text-to-speech/v123?output_format=mp3_44100_128" {
		t.Errorf("path=%s", req.Path)
	}
	if !strings.Contains(string(req.Body), "olá mundo") {
		t.Errorf("body=%s", req.Body)
	}
}

func TestBuildOpSubmitElevenLabsTTSVozPadrao(t *testing.T) {
	req, err := buildOpSubmit(opAdapterElevenLabs, opTTS, apiRouterOpInput{Text: "oi"})
	if err != nil {
		t.Fatalf("buildOpSubmit: %v", err)
	}
	if !strings.HasPrefix(req.Path, "/v1/text-to-speech/21m00Tcm4TlvDq8ikWAM") {
		t.Errorf("path=%s (voz padrão esperada)", req.Path)
	}
}

func TestBuildOpSubmitStability(t *testing.T) {
	req, err := buildOpSubmit(opAdapterStability, opImage, apiRouterOpInput{Prompt: "um gato astronauta", AspectRatio: "16:9", NegativePrompt: "blur"})
	if err != nil {
		t.Fatalf("buildOpSubmit: %v", err)
	}
	if req.Path != "/v2beta/stable-image/generate/core" {
		t.Errorf("path=%s", req.Path)
	}
	if req.Headers["Accept"] != "application/json" {
		t.Errorf("Accept=%q", req.Headers["Accept"])
	}
	if !strings.Contains(req.ContentType, "multipart/form-data") {
		t.Errorf("contentType=%s", req.ContentType)
	}
	if !strings.Contains(string(req.Body), "um gato astronauta") {
		t.Errorf("body=%s", req.Body)
	}
}

func TestBuildOpSubmitReplicate(t *testing.T) {
	req, err := buildOpSubmit(opAdapterReplicate, opPredict, apiRouterOpInput{Version: "abcd1234", Prompt: "um poema"})
	if err != nil {
		t.Fatalf("buildOpSubmit: %v", err)
	}
	if req.Path != "/v1/predictions" {
		t.Errorf("path=%s", req.Path)
	}
	if !strings.Contains(string(req.Body), "abcd1234") || !strings.Contains(string(req.Body), "um poema") {
		t.Errorf("body=%s", req.Body)
	}
	if !opAsync(opAdapterReplicate, opPredict) {
		t.Error("replicate predict deveria ser assíncrono")
	}
}

func TestOpCapabilities(t *testing.T) {
	if !containsString(apiRouterOpCapabilities[opAdapterAssemblyAI], opTranscribe) {
		t.Error("assemblyai deveria ter transcribe")
	}
	if !containsString(apiRouterOpCapabilities[opAdapterElevenLabs], opTTS) {
		t.Error("elevenlabs deveria ter tts")
	}
	if !containsString(apiRouterOpCapabilities[opAdapterStability], opImage) {
		t.Error("stability deveria ter image")
	}
	if !containsString(apiRouterOpCapabilities[opAdapterReplicate], opPredict) {
		t.Error("replicate deveria ter predict")
	}
	if containsString(apiRouterOpCapabilities[opAdapterAssemblyAI], opImage) {
		t.Error("assemblyai não deveria ter image")
	}
}

func TestOpJobID(t *testing.T) {
	id, err := opJobID(opAdapterAssemblyAI, opTranscribe, []byte(`{"id":"job_abc","status":"queued"}`))
	if err != nil {
		t.Fatalf("opJobID: %v", err)
	}
	if id != "job_abc" {
		t.Errorf("id=%s", id)
	}
	if _, err := opJobID(opAdapterAssemblyAI, opTranscribe, []byte(`{"status":"queued"}`)); err == nil {
		t.Error("esperava erro sem id")
	}
}

func TestOpPollStatus(t *testing.T) {
	cases := []struct {
		adapter, op string
		body        string
		done        bool
		err         string
	}{
		{opAdapterAssemblyAI, opTranscribe, `{"status":"queued"}`, false, ""},
		{opAdapterAssemblyAI, opTranscribe, `{"status":"processing"}`, false, ""},
		{opAdapterAssemblyAI, opTranscribe, `{"status":"completed","text":"oi"}`, true, ""},
		{opAdapterAssemblyAI, opTranscribe, `{"status":"error","error":"audio corrompido"}`, true, "audio corrompido"},
		{opAdapterReplicate, opPredict, `{"status":"starting"}`, false, ""},
		{opAdapterReplicate, opPredict, `{"status":"processing"}`, false, ""},
		{opAdapterReplicate, opPredict, `{"status":"succeeded","output":"42"}`, true, ""},
		{opAdapterReplicate, opPredict, `{"status":"failed","error":"sem crédito"}`, true, "sem crédito"},
	}
	for _, c := range cases {
		done, errMsg := opPollStatus(c.adapter, c.op, []byte(c.body))
		if done != c.done || errMsg != c.err {
			t.Errorf("%s/%s %s: done=%v err=%q, queria done=%v err=%q", c.adapter, c.op, c.body, done, errMsg, c.done, c.err)
		}
	}
}

func TestParseOpResultDeepgram(t *testing.T) {
	raw := []byte(`{"results":{"channels":[{"alternatives":[{"transcript":"olá mundo","confidence":0.98}]}]}}`)
	out, err := parseOpResult(opAdapterDeepgram, opTranscribe, raw)
	if err != nil {
		t.Fatalf("parseOpResult: %v", err)
	}
	if out["text"] != "olá mundo" {
		t.Errorf("text=%v", out["text"])
	}
	if out["confidence"] != 0.98 {
		t.Errorf("confidence=%v", out["confidence"])
	}
}

func TestParseOpResultAssemblyAI(t *testing.T) {
	raw := []byte(`{"status":"completed","text":"oi","language_code":"pt","confidence":0.9,"audio_duration":3.2}`)
	out, err := parseOpResult(opAdapterAssemblyAI, opTranscribe, raw)
	if err != nil {
		t.Fatalf("parseOpResult: %v", err)
	}
	if out["text"] != "oi" || out["language"] != "pt" {
		t.Errorf("out=%v", out)
	}
}

func TestParseOpResultElevenLabsTTS(t *testing.T) {
	out, err := parseOpResult(opAdapterElevenLabs, opTTS, []byte("ID3FAKEAUDIO"))
	if err != nil {
		t.Fatalf("parseOpResult: %v", err)
	}
	if out["contentType"] != "audio/mpeg" {
		t.Errorf("contentType=%v", out["contentType"])
	}
	if out["audioB64"] != base64.StdEncoding.EncodeToString([]byte("ID3FAKEAUDIO")) {
		t.Errorf("audioB64=%v", out["audioB64"])
	}
}

func TestParseOpResultElevenLabsSTT(t *testing.T) {
	out, err := parseOpResult(opAdapterElevenLabs, opTranscribe, []byte(`{"text":"transcrição"}`))
	if err != nil {
		t.Fatalf("parseOpResult: %v", err)
	}
	if out["text"] != "transcrição" {
		t.Errorf("text=%v", out["text"])
	}
}

func TestParseOpResultStabilityJSON(t *testing.T) {
	raw := []byte(`{"image":"YmFzZTY0aW1n","finish_reason":"SUCCESS","seed":123}`)
	out, err := parseOpResult(opAdapterStability, opImage, raw)
	if err != nil {
		t.Fatalf("parseOpResult: %v", err)
	}
	if out["imageB64"] != "YmFzZTY0aW1n" || out["finishReason"] != "SUCCESS" {
		t.Errorf("out=%v", out)
	}
}

func TestParseOpResultStabilityBinario(t *testing.T) {
	out, err := parseOpResult(opAdapterStability, opImage, []byte("PNGRAW"))
	if err != nil {
		t.Fatalf("parseOpResult: %v", err)
	}
	if out["imageB64"] != base64.StdEncoding.EncodeToString([]byte("PNGRAW")) {
		t.Errorf("imageB64=%v", out["imageB64"])
	}
}

func TestParseOpResultReplicate(t *testing.T) {
	out, err := parseOpResult(opAdapterReplicate, opPredict, []byte(`{"status":"succeeded","output":"um poema","error":null}`))
	if err != nil {
		t.Fatalf("parseOpResult: %v", err)
	}
	if out["output"] != "um poema" {
		t.Errorf("output=%v", out["output"])
	}
}

func TestParseOpResultReplicateErro(t *testing.T) {
	if _, err := parseOpResult(opAdapterReplicate, opPredict, []byte(`{"status":"failed","error":"insufficient balance"}`)); err == nil {
		t.Error("esperava erro em replicate failed")
	}
}

func TestParseOpResultVoices(t *testing.T) {
	raw := []byte(`{"voices":[{"voice_id":"v1","name":"Rachel"},{"voice_id":"v2","name":"Adam"}]}`)
	out, err := parseOpResult(opAdapterElevenLabs, opVoices, raw)
	if err != nil {
		t.Fatalf("parseOpResult: %v", err)
	}
	voices, ok := out["voices"].([]map[string]any)
	if !ok || len(voices) != 2 || voices[0]["id"] != "v1" {
		t.Errorf("voices=%v", out["voices"])
	}
}

func TestContainsString(t *testing.T) {
	if !containsString([]string{"a", "b"}, "a") || containsString([]string{"a"}, "x") {
		t.Error("containsString errado")
	}
}
