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
	raw := []byte(`{"content":[{"type":"text","text":"{\"answer\":\"B\",\"reasoning\":\"Ulan Bator é a capital.\"}"}]}`)
	p := quizParsed{Options: map[string]string{"A": "Astana", "B": "Ulan Bator"}}
	got, err := parseFallbackAnswer(raw, p)
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
	raw := []byte(`{"content":[{"type":"text","text":"Claro!\n` + "```json" + `\n{\"answer\": \"A\", \"reasoning\": \"porque sim\"}\n` + "```" + `"}]}`)
	p := quizParsed{Options: map[string]string{"A": "Astana", "B": "Ulan Bator"}}
	got, err := parseFallbackAnswer(raw, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswer: %v", err)
	}
	if got.Label != "A" {
		t.Errorf("label = %q", got.Label)
	}
}

func TestParseFallbackAnswerRotuloDesconhecido(t *testing.T) {
	raw := []byte(`{"content":[{"type":"text","text":"{\"answer\":\"Z\"}"}]}`)
	p := quizParsed{Options: map[string]string{"A": "Astana"}}
	if _, err := parseFallbackAnswer(raw, p); err == nil {
		t.Error("queria erro para rótulo fora do conjunto")
	}
}
