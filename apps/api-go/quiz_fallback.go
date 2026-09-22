package main

// Escalonamento: quando o Jev fica inseguro, a questão vai para o Claude Code
// rodando em container (apps/agent-go), via apps/api-go/agent_client.go — sem
// chave de API, o container usa a assinatura da empresa. O prompt pede JSON
// estrito porque a resposta precisa virar uma alternativa, não um parágrafo.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
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
	// O placeholder genérico "<rótulo exatamente como listado acima>" já foi
	// lido por um modelo como "a linha inteira da alternativa" em produção
	// (ver task-4-report.md, Hotfix produção). Exemplificar com o primeiro
	// rótulo REAL desta questão não deixa margem de interpretação: o campo
	// "answer" é só a letra/número, nunca o texto da alternativa.
	exemplo := "A"
	if len(p.Order) > 0 {
		exemplo = p.Order[0]
	}
	b.WriteString("\nResponda SOMENTE com um objeto JSON. O campo \"answer\" é APENAS o rótulo ")
	b.WriteString("da alternativa (a letra ou número, nunca o texto dela). Exemplo, usando o ")
	fmt.Fprintf(&b, "primeiro rótulo desta questão: {\"answer\": %q, \"reasoning\": \"<uma frase curta>\"}", exemplo)
	b.WriteString(".\nNão repita o texto da alternativa em \"answer\" nem escreva nada fora do JSON.")
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
	label, ok := resolveFallbackLabel(ans.Label, p.Options)
	if !ok {
		return quizFallbackAnswer{}, fmt.Errorf("quiz: fallback devolveu rótulo desconhecido %q", truncateQuizLabel(ans.Label))
	}
	ans.Label = label
	return ans, nil
}

// quizLabelTruncateLen é o teto de caracteres do rótulo cru citado na
// mensagem de erro. O caso de produção que motivou isso devolveu o texto
// inteiro da alternativa no campo "answer" — sem teto, o log vira ruído.
const quizLabelTruncateLen = 80

func truncateQuizLabel(s string) string {
	r := []rune(s)
	if len(r) <= quizLabelTruncateLen {
		return s
	}
	return string(r[:quizLabelTruncateLen]) + "…"
}

// resolveFallbackLabel tolera os formatos mais comuns que um LLM devolve no
// campo "answer" mesmo quando o prompt pede só o rótulo — ex.: "C) texto da
// alternativa inteiro" em vez de "C" (bug de produção: o modelo leu "o
// rótulo exatamente como listado acima" como "a linha inteira"). Nunca
// inventa um rótulo: só devolve um resultado que já exista em options —
// aceitar qualquer prefixo faria a extensão mostrar uma letra que não está
// na prova.
func resolveFallbackLabel(label string, options map[string]string) (string, bool) {
	// (a) casamento exato.
	if _, ok := options[label]; ok {
		return label, true
	}
	// (b) depois de strings.TrimSpace.
	trimmed := strings.TrimSpace(label)
	if _, ok := options[trimmed]; ok {
		return trimmed, true
	}
	// (c) tokens separados por ')', '.', '-', ':' ou espaço, na ordem em que
	// aparecem no texto. Varremos todos os tokens (não só o primeiro) porque
	// tanto "C) texto da alternativa" (rótulo primeiro) quanto "Alternativa
	// C" (rótulo depois de uma palavra) são respostas plausíveis de um LLM —
	// o primeiro token que bater com uma chave real vence.
	tokens := quizFallbackLabelTokens(trimmed)
	for _, tok := range tokens {
		if _, ok := options[tok]; ok {
			return tok, true
		}
	}
	// (d) casamento sem diferenciar maiúscula/minúscula contra as chaves
	// existentes — cobre " c " e variações de caixa dentro dos tokens.
	if key, ok := quizFallbackFoldMatch(trimmed, options); ok {
		return key, true
	}
	for _, tok := range tokens {
		if key, ok := quizFallbackFoldMatch(tok, options); ok {
			return key, true
		}
	}
	return "", false
}

func quizFallbackLabelTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ')' || r == '.' || r == '-' || r == ':' || unicode.IsSpace(r)
	})
}

// quizFallbackFoldMatch compara s contra as chaves de options ignorando
// caixa. Itera em ordem alfabética (mapa em Go não tem ordem) pra um
// eventual empate de case-fold dar sempre o mesmo resultado.
func quizFallbackFoldMatch(s string, options map[string]string) (string, bool) {
	if s == "" {
		return "", false
	}
	keys := make([]string, 0, len(options))
	for k := range options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.EqualFold(k, s) {
			return k, true
		}
	}
	return "", false
}
