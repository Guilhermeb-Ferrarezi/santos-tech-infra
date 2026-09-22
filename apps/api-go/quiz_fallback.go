package main

// Escalonamento: quando o Jev fica inseguro, a questão vai para o Claude Code
// rodando em container (apps/agent-go), via apps/api-go/agent_client.go — sem
// chave de API, o container usa a assinatura da empresa. O prompt pede JSON
// estrito porque a resposta precisa virar uma alternativa, não um parágrafo.

import (
	"encoding/json"
	"fmt"
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

// primeiroObjetoJSON devolve o primeiro objeto JSON completo do texto.
// Uma regex gulosa não serve: ela casaria do primeiro "{" ao último "}",
// e o prompt manda um exemplo de JSON que o modelo às vezes ecoa antes da
// resposta — o match viraria os dois objetos com texto no meio.
func primeiroObjetoJSON(texto string) string {
	inicio, profundidade := -1, 0
	emString, escape := false, false
	for i, r := range texto {
		if emString {
			switch {
			case escape:
				escape = false
			case r == '\\':
				escape = true
			case r == '"':
				emString = false
			}
			continue
		}
		switch r {
		case '"':
			emString = true
		case '{':
			if profundidade == 0 {
				inicio = i
			}
			profundidade++
		case '}':
			profundidade--
			if profundidade == 0 && inicio >= 0 {
				return texto[inicio : i+1]
			}
		}
	}
	return ""
}

func parseFallbackAnswer(texto string, p quizParsed) (quizFallbackAnswer, error) {
	if strings.TrimSpace(texto) == "" {
		return quizFallbackAnswer{}, fmt.Errorf("quiz: fallback não devolveu texto")
	}
	match := primeiroObjetoJSON(texto)
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
