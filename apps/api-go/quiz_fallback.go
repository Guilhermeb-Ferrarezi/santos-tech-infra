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

// buildFallbackPrompt monta o prompt do fallback. temImagem indica que uma
// figura (gráfico, cupom, tabela, figura geométrica) foi anexada à
// requisição — nesse caso o modelo precisa ser instruído a examiná-la, já
// que o enunciado sozinho pode não bastar pra responder.
func buildFallbackPrompt(p quizParsed, temImagem bool) string {
	var b strings.Builder
	b.WriteString("Responda a questão de múltipla escolha abaixo.\n\n")
	if temImagem {
		b.WriteString("Há uma imagem anexada a esta mensagem — examine-a com atenção antes de ")
		b.WriteString("responder. O enunciado sozinho pode não bastar: a resposta pode depender de ")
		b.WriteString("um gráfico, uma tabela, um cupom ou uma figura geométrica presente na imagem.\n\n")
	}
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

// ── modo múltipla resposta ("marque todas que se aplicam") ────────────────
//
// buildFallbackPromptMultipla e parseFallbackAnswerMultipla são irmãs de
// buildFallbackPrompt/parseFallbackAnswer, mas pedem/aceitam uma LISTA de
// rótulos em vez de um só. Funções separadas (não um parâmetro a mais nas
// existentes) de propósito: os testes de buildFallbackPrompt/parseFallbackAnswer
// continuam chamando as funções originais sem editar assinatura nenhuma.

// quizFallbackAnswerMultipla: resposta do Claude no modo múltipla resposta.
// Labels vem de "answers" (o formato pedido no prompt); Answer é um alias de
// compatibilidade pro campo singular "answer" — um modelo às vezes devolve
// isso mesmo quando o prompt pede lista, e a regra é aceitar como lista de
// um elemento em vez de recusar a resposta inteira.
type quizFallbackAnswerMultipla struct {
	Labels    []string `json:"answers"`
	Answer    string   `json:"answer"`
	Reasoning string   `json:"reasoning"`
}

// buildFallbackPromptMultipla monta o prompt do modo múltipla resposta.
// temImagem segue o mesmo significado de buildFallbackPrompt.
func buildFallbackPromptMultipla(p quizParsed, temImagem bool) string {
	var b strings.Builder
	b.WriteString("Responda a questão de MÚLTIPLA RESPOSTA abaixo — marque TODAS as alternativas ")
	b.WriteString("corretas, pode ser mais de uma.\n\n")
	if temImagem {
		b.WriteString("Há uma imagem anexada a esta mensagem — examine-a com atenção antes de ")
		b.WriteString("responder. O enunciado sozinho pode não bastar: a resposta pode depender de ")
		b.WriteString("um gráfico, uma tabela, um cupom ou uma figura geométrica presente na imagem.\n\n")
	}
	b.WriteString(p.Question)
	b.WriteString("\n\n")
	for _, label := range p.Order {
		fmt.Fprintf(&b, "%s) %s\n", label, p.Options[label])
	}
	// Mesmo cuidado de buildFallbackPrompt (ver o comentário lá, Hotfix
	// produção): exemplificar com rótulos REAIS da questão, nunca um
	// placeholder genérico que um modelo possa ler como "a linha inteira".
	ex1, ex2 := "A", "C"
	switch len(p.Order) {
	case 0:
		// sem alternativas não deveria acontecer aqui (mode múltipla exige
		// alternativas), mas mantém os placeholders genéricos por segurança.
	case 1:
		ex1, ex2 = p.Order[0], p.Order[0]
	default:
		ex1, ex2 = p.Order[0], p.Order[len(p.Order)-1]
	}
	b.WriteString("\nResponda SOMENTE com um objeto JSON. O campo \"answers\" é uma LISTA com TODOS ")
	b.WriteString("os rótulos corretos (a letra ou número de cada alternativa, nunca o texto dela). ")
	b.WriteString("Exemplo, usando rótulos desta questão: ")
	fmt.Fprintf(&b, "{\"answers\": [%q, %q], \"reasoning\": \"<uma frase curta>\"}", ex1, ex2)
	b.WriteString(".\nNão repita o texto das alternativas em \"answers\" nem escreva nada fora do JSON.")
	return b.String()
}

// parseFallbackAnswerMultipla extrai a lista de rótulos corretos. Cada
// rótulo passa por resolveFallbackLabel (mesma tolerância de formato do modo
// escolha única — "C) texto…", caixa diferente etc). Um rótulo inválido é
// DESCARTADO, não invalida a resposta inteira: melhor devolver os válidos
// que jogar tudo fora por causa de um só ruim (decisão explícita da spec,
// diferente de parseFallbackAnswer no modo escolha única, onde um único
// rótulo TEM que ser válido). Nunca devolve lista vazia com sucesso: sem
// nenhum rótulo válido, é erro (equivalente a "fallback não respondeu").
func parseFallbackAnswerMultipla(texto string, p quizParsed) (quizFallbackAnswerMultipla, error) {
	if strings.TrimSpace(texto) == "" {
		return quizFallbackAnswerMultipla{}, fmt.Errorf("quiz: fallback não devolveu texto")
	}
	match := primeiroObjetoJSON(texto)
	if match == "" {
		return quizFallbackAnswerMultipla{}, fmt.Errorf("quiz: fallback não devolveu JSON")
	}
	var ans quizFallbackAnswerMultipla
	if err := json.Unmarshal([]byte(match), &ans); err != nil {
		return quizFallbackAnswerMultipla{}, fmt.Errorf("quiz: JSON do fallback inválido: %w", err)
	}
	brutos := ans.Labels
	if len(brutos) == 0 && ans.Answer != "" {
		// Modelo devolveu "answer" string em vez de "answers" lista — aceita
		// como lista de um elemento.
		brutos = []string{ans.Answer}
	}
	validos := make([]string, 0, len(brutos))
	for _, bruto := range brutos {
		label, ok := resolveFallbackLabel(bruto, p.Options)
		if !ok {
			continue // descartado, não invalida a resposta inteira
		}
		duplicado := false
		for _, v := range validos {
			if v == label {
				duplicado = true
				break
			}
		}
		if !duplicado {
			validos = append(validos, label)
		}
	}
	if len(validos) == 0 {
		return quizFallbackAnswerMultipla{}, fmt.Errorf("quiz: fallback não devolveu nenhum rótulo válido")
	}
	// Reordena por p.Order (não pela ordem em que o modelo escreveu) — o
	// card mostra os rótulos nessa ordem, e a ordem do LLM não é confiável.
	ordenados := make([]string, 0, len(validos))
	for _, label := range p.Order {
		for _, v := range validos {
			if v == label {
				ordenados = append(ordenados, label)
				break
			}
		}
	}
	return quizFallbackAnswerMultipla{Labels: ordenados, Reasoning: ans.Reasoning}, nil
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

// ── modo aberto ──────────────────────────────────────────────────────────
//
// Questão sem alternativas reconhecíveis (dissertativa, preencher lacuna):
// em vez de recusar com 422, manda o texto direto ao Claude e devolve a
// resposta em texto livre. buildOpenPrompt e parseOpenAnswer são irmãs de
// buildFallbackPrompt e parseFallbackAnswer, mas sem rótulo — não há
// alternativa nenhuma pra escolher, só texto pra responder.

type quizOpenAnswer struct {
	Answer    string `json:"answer"`
	Reasoning string `json:"reasoning"`
}

// quizOpenAnswerMaxChars: teto pra resposta caber no overlay da extensão —
// mesma motivação de quizLabelTruncateLen (não deixar o log/UI virar ruído
// com um texto arbitrariamente longo), mas aplicado à resposta em si, não a
// um rótulo cru citado num erro. 600 caracteres cobre um parágrafo curto de
// verdade (1–3 frases, como o prompt pede) com folga.
const quizOpenAnswerMaxChars = 600

func truncateQuizOpenAnswer(s string) string {
	r := []rune(s)
	if len(r) <= quizOpenAnswerMaxChars {
		return s
	}
	return string(r[:quizOpenAnswerMaxChars]) + "…"
}

// buildOpenPrompt monta o prompt do modo aberto. temImagem segue o mesmo
// significado de buildFallbackPrompt: há uma figura anexada que o modelo
// precisa considerar.
func buildOpenPrompt(texto string, temImagem bool) string {
	var b strings.Builder
	b.WriteString("Responda a questão abaixo de forma DIRETA e CURTA, em 1 a 3 frases, em português do Brasil.\n\n")
	b.WriteString("Se a questão for de preencher lacunas, devolva as palavras que preenchem as lacunas, ")
	b.WriteString("na ordem em que aparecem no enunciado — não escreva uma dissertação.\n\n")
	// Correção de produção: numa questão de completar lacunas ou de apontar
	// informação do texto, a correção costuma exigir a palavra exata do
	// texto de apoio, não um sinônimo semanticamente equivalente — mesmo um
	// sinônimo correto ("informar" no lugar de "orientar") perde ponto numa
	// prova objetiva. Instrui explicitamente essa preferência: usar
	// conhecimento externo só quando o próprio texto não oferecer a resposta.
	b.WriteString("Se a questão for de completar lacunas ou de identificar uma informação presente no ")
	b.WriteString("texto selecionado, PREFIRA as palavras que já aparecem nesse texto em vez de ")
	b.WriteString("sinônimos — mesmo um sinônimo correto costuma não valer ponto numa correção objetiva. ")
	b.WriteString("Só recorra a conhecimento externo quando o texto não oferecer a resposta.\n\n")
	if temImagem {
		b.WriteString("Há uma imagem anexada a esta mensagem — considere-a ao responder. O enunciado ")
		b.WriteString("sozinho pode não bastar: a resposta pode depender de um gráfico, uma tabela, um ")
		b.WriteString("cupom ou uma figura geométrica presente na imagem.\n\n")
	}
	b.WriteString(texto)
	b.WriteString("\n\n")
	b.WriteString("Responda SOMENTE com um objeto JSON estrito, sem nada fora dele: ")
	b.WriteString(`{"answer": "<a resposta>", "reasoning": "<uma frase curta, opcional>"}`)
	b.WriteString(".")
	return b.String()
}

// parseOpenAnswer extrai a resposta do modo aberto. Diferente de
// parseFallbackAnswer, não valida nada contra p.Options — não existem
// opções no modo aberto, então não há rótulo pra checar. Tolerante de
// propósito: se o modelo não devolver JSON nenhum, ou devolver um JSON mal
// formado / sem o campo "answer", o texto cru ainda é aproveitado como
// resposta — no modo aberto, texto solto é uma resposta útil; no modo
// múltipla um rótulo inválido não seria (por isso parseFallbackAnswer
// recusa e este não).
func parseOpenAnswer(texto string) (quizOpenAnswer, error) {
	limpo := strings.TrimSpace(texto)
	if limpo == "" {
		return quizOpenAnswer{}, fmt.Errorf("quiz: fallback não devolveu texto")
	}
	if match := primeiroObjetoJSON(texto); match != "" {
		var ans quizOpenAnswer
		if err := json.Unmarshal([]byte(match), &ans); err == nil {
			if resposta := strings.TrimSpace(ans.Answer); resposta != "" {
				ans.Answer = truncateQuizOpenAnswer(resposta)
				return ans, nil
			}
		}
	}
	// Sem JSON utilizável (ausente, malformado, ou sem "answer"): o texto
	// cru da resposta ainda serve.
	return quizOpenAnswer{Answer: truncateQuizOpenAnswer(limpo)}, nil
}
