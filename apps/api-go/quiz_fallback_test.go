package main

import (
	"strings"
	"testing"
)

func TestBuildFallbackPromptListaAlternativasNaOrdem(t *testing.T) {
	p := quizParsed{
		Question: "Qual a capital da Mongólia?",
		Options:  map[string]string{"A": "Astana", "B": "Ulan Bator"},
		Order:    []string{"A", "B"},
	}
	got := buildFallbackPrompt(p)
	if !strings.Contains(got, "Qual a capital da Mongólia?") {
		t.Error("prompt sem o enunciado")
	}
	iA, iB := strings.Index(got, "A) Astana"), strings.Index(got, "B) Ulan Bator")
	if iA < 0 || iB < 0 || iA > iB {
		// Ordem embaralhada muda a resposta de um LLM; Order existe pra isso.
		t.Errorf("alternativas fora de ordem no prompt:\n%s", got)
	}
	if !strings.Contains(got, "JSON") {
		t.Error("prompt não pede JSON — o parse depende disso")
	}
}

func TestParseFallbackAnswerRespostaNativaAnthropic(t *testing.T) {
	raw := []byte(`{"content":[{"text":"{\"answer\":\"B\",\"reasoning\":\"Ulan Bator é a capital.\"}"}]}`)
	p := quizParsed{Options: map[string]string{"A": "Astana", "B": "Ulan Bator"}}
	got, err := parseFallbackAnswer(chatAdapterAnthropic, raw, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswer: %v", err)
	}
	if got.Label != "B" || got.Reasoning == "" {
		t.Errorf("resposta = %+v", got)
	}
}

func TestParseFallbackAnswerJSONComCercaDeCodigo(t *testing.T) {
	// Modelo de texto costuma embrulhar o JSON em ```json ... ```; aceitar isso
	// evita transformar uma resposta boa em erro.
	raw := []byte(`{"content":[{"text":"Claro!\n` + "```json" + `\n{\"answer\": \"A\", \"reasoning\": \"porque sim\"}\n` + "```" + `"}]}`)
	p := quizParsed{Options: map[string]string{"A": "Astana", "B": "Ulan Bator"}}
	got, err := parseFallbackAnswer(chatAdapterAnthropic, raw, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswer: %v", err)
	}
	if got.Label != "A" {
		t.Errorf("label = %q", got.Label)
	}
}

func TestParseFallbackAnswerRotuloDesconhecido(t *testing.T) {
	raw := []byte(`{"content":[{"text":"{\"answer\":\"Z\"}"}]}`)
	p := quizParsed{Options: map[string]string{"A": "Astana"}}
	if _, err := parseFallbackAnswer(chatAdapterAnthropic, raw, p); err == nil {
		t.Error("queria erro para rótulo fora do conjunto")
	}
}

func TestParseFallbackAnswerEcoDoExemploAntesDaResposta(t *testing.T) {
	// O prompt inclui um exemplo JSON literal. Modelo de texto às vezes o ecoa
	// antes de responder com o JSON real. O primeiro objeto JSON vence — nesse
	// caso é o echo do exemplo, cujo answer não é um rótulo válido, logo erro.
	exemplo := `{"answer": "<rótulo exatamente como listado acima>", "reasoning": "<uma frase curta>"}`
	resposta := `{"answer":"A","reasoning":"Resposta correta"}`
	texto := exemplo + " " + resposta
	raw := []byte(`{"content":[{"text":"` + strings.ReplaceAll(texto, `"`, `\"`) + `"}]}`)
	p := quizParsed{Options: map[string]string{"A": "Astana"}}
	_, err := parseFallbackAnswer(chatAdapterAnthropic, raw, p)
	if err == nil || !strings.Contains(err.Error(), "desconhecido") {
		t.Errorf("esperava erro de rótulo desconhecido, got: %v", err)
	}
}

func TestParseFallbackAnswerTextoDepoisDoJSON(t *testing.T) {
	// JSON no meio com conversa depois é válido — extrair o primeiro JSON.
	raw := []byte(`{"content":[{"text":"{\"answer\":\"A\",\"reasoning\":\"x\"} Espero ter ajudado!"}]}`)
	p := quizParsed{Options: map[string]string{"A": "Astana"}}
	got, err := parseFallbackAnswer(chatAdapterAnthropic, raw, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswer: %v", err)
	}
	if got.Label != "A" {
		t.Errorf("label = %q", got.Label)
	}
}

func TestParseFallbackAnswerErroDoProviderPropagado(t *testing.T) {
	// Se o provider devolver erro (rate limit, modelo inválido, etc), a
	// mensagem de erro real deve aparecer, não um genérico "sem texto".
	raw := []byte(`{"error":{"message":"rate limit exceeded"}}`)
	p := quizParsed{Options: map[string]string{"A": "Astana"}}
	_, err := parseFallbackAnswer(chatAdapterAnthropic, raw, p)
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("esperava erro com 'rate limit', got: %v", err)
	}
}

func TestParseFallbackAnswerChaveEmString(t *testing.T) {
	// Um reasoning com "{" dentro não quebra a extração — contagem de
	// profundidade respeita aspas.
	raw := []byte(`{"content":[{"text":"{\"answer\":\"A\",\"reasoning\":\"Tem {chaves} dentro\"}"}]}`)
	p := quizParsed{Options: map[string]string{"A": "Astana"}}
	got, err := parseFallbackAnswer(chatAdapterAnthropic, raw, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswer: %v", err)
	}
	if got.Label != "A" || !strings.Contains(got.Reasoning, "{") {
		t.Errorf("resposta = %+v", got)
	}
}
