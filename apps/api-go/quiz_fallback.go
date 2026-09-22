package main

// Escalonamento: quando o Jev fica inseguro, a questão vai para um LLM de
// texto pelo adapter `anthropic` do API Router. O prompt pede JSON estrito
// porque a resposta precisa virar uma alternativa, não um parágrafo.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type quizFallbackAnswer struct {
	Label     string `json:"answer"`
	Reasoning string `json:"reasoning"`
}

func buildFallbackPrompt(p quizParsed) string {
	var b strings.Builder
	b.WriteString("Responda a questão de múltipla escolha abaixo.\n\n")
	b.WriteString(p.Question)
	b.WriteString("\n\n")
	for _, label := range p.Order {
		fmt.Fprintf(&b, "%s) %s\n", label, p.Options[label])
	}
	b.WriteString("\nResponda SOMENTE com um objeto JSON no formato ")
	b.WriteString(`{"answer": "<rótulo exatamente como listado acima>", "reasoning": "<uma frase curta>"}`)
	b.WriteString(".\nNão escreva nada fora do JSON.")
	return b.String()
}

// quizJSONRe acha o primeiro objeto JSON do texto, mesmo embrulhado em cerca de
// código ou precedido de conversa fiada.
var quizJSONRe = regexp.MustCompile(`(?s)\{.*\}`)

func parseFallbackAnswer(raw []byte, p quizParsed) (quizFallbackAnswer, error) {
	var native struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &native); err != nil {
		return quizFallbackAnswer{}, fmt.Errorf("quiz: resposta do fallback ilegível: %w", err)
	}
	text := ""
	for _, c := range native.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	if text == "" {
		return quizFallbackAnswer{}, fmt.Errorf("quiz: fallback não devolveu texto")
	}
	match := quizJSONRe.FindString(text)
	if match == "" {
		return quizFallbackAnswer{}, fmt.Errorf("quiz: fallback não devolveu JSON")
	}
	var ans quizFallbackAnswer
	if err := json.Unmarshal([]byte(match), &ans); err != nil {
		return quizFallbackAnswer{}, fmt.Errorf("quiz: JSON do fallback inválido: %w", err)
	}
	if _, ok := p.Options[ans.Label]; !ok {
		return quizFallbackAnswer{}, fmt.Errorf("quiz: fallback devolveu rótulo desconhecido %q", ans.Label)
	}
	return ans, nil
}
