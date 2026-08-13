package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildChatRequestOpenAICompatible(t *testing.T) {
	path, body, headers, err := buildChatRequest(chatAdapterOpenAICompatible, "", "olá")
	if err != nil {
		t.Fatalf("buildChatRequest: %v", err)
	}
	if path != "/v1/chat/completions" {
		t.Errorf("path=%q", path)
	}
	if headers != nil {
		t.Errorf("headers=%v, queria nil (auth já vem do provider)", headers)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	if decoded["model"] != "gpt-4o-mini" {
		t.Errorf("model default = %v", decoded["model"])
	}
	msgs, _ := decoded["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages=%v", decoded["messages"])
	}
	msg := msgs[0].(map[string]any)
	if msg["role"] != "user" || msg["content"] != "olá" {
		t.Errorf("mensagem = %v", msg)
	}
}

func TestBuildChatRequestOpenAICompatibleModelExplicito(t *testing.T) {
	_, body, _, err := buildChatRequest(chatAdapterOpenAICompatible, "gpt-4.1", "oi")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	if decoded["model"] != "gpt-4.1" {
		t.Errorf("model explícito não preservado: %v", decoded["model"])
	}
}

func TestBuildChatRequestAnthropic(t *testing.T) {
	path, body, headers, err := buildChatRequest(chatAdapterAnthropic, "", "olá")
	if err != nil {
		t.Fatalf("buildChatRequest: %v", err)
	}
	if path != "/v1/messages" {
		t.Errorf("path=%q", path)
	}
	if headers["anthropic-version"] == "" {
		t.Error("faltou o header anthropic-version (obrigatório na API da Anthropic)")
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	if decoded["max_tokens"] == nil {
		t.Error("max_tokens é obrigatório na API da Anthropic e não foi setado")
	}
}

func TestBuildChatRequestGoogle(t *testing.T) {
	path, body, _, err := buildChatRequest(chatAdapterGoogle, "gemini-2.5-pro", "olá")
	if err != nil {
		t.Fatalf("buildChatRequest: %v", err)
	}
	if !strings.Contains(path, "gemini-2.5-pro:generateContent") {
		t.Errorf("path não carrega o modelo: %q", path)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	if decoded["contents"] == nil {
		t.Error("body google sem 'contents'")
	}
}

func TestBuildChatRequestAdapterDesconhecido(t *testing.T) {
	if _, _, _, err := buildChatRequest("nao-existe", "", "oi"); err == nil {
		t.Fatal("esperava erro pra adapter desconhecido")
	}
}

func TestParseChatResponseOpenAICompatible(t *testing.T) {
	raw := []byte(`{"choices":[{"message":{"content":"olá, tudo bem?"}}]}`)
	text, err := parseChatResponse(chatAdapterOpenAICompatible, raw)
	if err != nil {
		t.Fatal(err)
	}
	if text != "olá, tudo bem?" {
		t.Errorf("text=%q", text)
	}
}

func TestParseChatResponseOpenAICompatibleErro(t *testing.T) {
	raw := []byte(`{"error":{"message":"invalid api key"}}`)
	if _, err := parseChatResponse(chatAdapterOpenAICompatible, raw); err == nil {
		t.Fatal("esperava erro quando o provider devolve {error:...}")
	}
}

func TestParseChatResponseAnthropic(t *testing.T) {
	raw := []byte(`{"content":[{"type":"text","text":"oi!"}]}`)
	text, err := parseChatResponse(chatAdapterAnthropic, raw)
	if err != nil {
		t.Fatal(err)
	}
	if text != "oi!" {
		t.Errorf("text=%q", text)
	}
}

func TestParseChatResponseGoogleConcatenaParts(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"oi, "},{"text":"tudo bem?"}]}}]}`)
	text, err := parseChatResponse(chatAdapterGoogle, raw)
	if err != nil {
		t.Fatal(err)
	}
	if text != "oi, tudo bem?" {
		t.Errorf("text=%q", text)
	}
}

func TestParseChatResponseSemConteudo(t *testing.T) {
	if _, err := parseChatResponse(chatAdapterOpenAICompatible, []byte(`{"choices":[]}`)); err == nil {
		t.Fatal("esperava erro pra choices vazio")
	}
	if _, err := parseChatResponse(chatAdapterAnthropic, []byte(`{"content":[]}`)); err == nil {
		t.Fatal("esperava erro pra content vazio")
	}
	if _, err := parseChatResponse(chatAdapterGoogle, []byte(`{"candidates":[]}`)); err == nil {
		t.Fatal("esperava erro pra candidates vazio")
	}
}
