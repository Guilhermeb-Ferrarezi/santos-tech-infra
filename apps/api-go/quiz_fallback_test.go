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
	got := buildFallbackPrompt(p, false)
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

// Hotfix produção: o placeholder genérico "<rótulo exatamente como listado
// acima>" foi lido por um modelo como "a linha inteira da alternativa". O
// prompt agora exemplifica com o primeiro rótulo REAL da questão, pra não
// deixar margem de interpretação.
func TestBuildFallbackPromptExemploUsaPrimeiroRotuloReal(t *testing.T) {
	p := quizParsed{
		Question: "Pergunta?",
		Options:  map[string]string{"X": "primeira", "Y": "segunda"},
		Order:    []string{"X", "Y"},
	}
	got := buildFallbackPrompt(p, false)
	if !strings.Contains(got, `"X"`) {
		t.Errorf("prompt não usa o primeiro rótulo real (X) no exemplo:\n%s", got)
	}
	if strings.Contains(got, "<rótulo exatamente como listado acima>") {
		t.Error("prompt ainda usa o placeholder genérico ambíguo")
	}
}

func TestBuildFallbackPromptComImagemMencionaAFigura(t *testing.T) {
	p := quizParsed{
		Question: "O que mostra o gráfico?",
		Options:  map[string]string{"A": "alta", "B": "baixa"},
		Order:    []string{"A", "B"},
	}
	got := buildFallbackPrompt(p, true)
	if !strings.Contains(strings.ToLower(got), "imagem") {
		t.Errorf("prompt com imagem não menciona a figura anexada:\n%s", got)
	}
}

func TestBuildFallbackPromptSemImagemNaoMencionaAFigura(t *testing.T) {
	p := quizParsed{
		Question: "Pergunta comum, sem figura?",
		Options:  map[string]string{"A": "x", "B": "y"},
		Order:    []string{"A", "B"},
	}
	got := buildFallbackPrompt(p, false)
	if strings.Contains(strings.ToLower(got), "imagem anexad") {
		t.Errorf("prompt sem imagem não deveria mencionar figura anexada:\n%s", got)
	}
}

func TestParseFallbackAnswerTextoSimples(t *testing.T) {
	texto := `{"answer":"B","reasoning":"Ulan Bator é a capital."}`
	p := quizParsed{Options: map[string]string{"A": "Astana", "B": "Ulan Bator"}}
	got, err := parseFallbackAnswer(texto, p)
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
	texto := "Claro!\n```json\n{\"answer\": \"A\", \"reasoning\": \"porque sim\"}\n```"
	p := quizParsed{Options: map[string]string{"A": "Astana", "B": "Ulan Bator"}}
	got, err := parseFallbackAnswer(texto, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswer: %v", err)
	}
	if got.Label != "A" {
		t.Errorf("label = %q", got.Label)
	}
}

func TestParseFallbackAnswerRotuloDesconhecido(t *testing.T) {
	texto := `{"answer":"Z"}`
	p := quizParsed{Options: map[string]string{"A": "Astana"}}
	if _, err := parseFallbackAnswer(texto, p); err == nil {
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
	p := quizParsed{Options: map[string]string{"A": "Astana"}}
	_, err := parseFallbackAnswer(texto, p)
	if err == nil || !strings.Contains(err.Error(), "desconhecido") {
		t.Errorf("esperava erro de rótulo desconhecido, got: %v", err)
	}
}

func TestParseFallbackAnswerTextoDepoisDoJSON(t *testing.T) {
	// JSON no meio com conversa depois é válido — extrair o primeiro JSON.
	texto := `{"answer":"A","reasoning":"x"} Espero ter ajudado!`
	p := quizParsed{Options: map[string]string{"A": "Astana"}}
	got, err := parseFallbackAnswer(texto, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswer: %v", err)
	}
	if got.Label != "A" {
		t.Errorf("label = %q", got.Label)
	}
}

func TestParseFallbackAnswerTextoVazio(t *testing.T) {
	// Sem envelope de provider, não há mais campo "error" pra propagar — o que
	// pode chegar aqui vazio é o texto cru do agent-go. Recusar com erro claro
	// em vez de tentar extrair JSON de nada.
	p := quizParsed{Options: map[string]string{"A": "Astana"}}
	if _, err := parseFallbackAnswer("", p); err == nil {
		t.Error("queria erro para texto vazio")
	}
}

func TestParseFallbackAnswerChaveEmString(t *testing.T) {
	// Um reasoning com "{" dentro não quebra a extração — contagem de
	// profundidade respeita aspas.
	texto := `{"answer":"A","reasoning":"Tem {chaves} dentro"}`
	p := quizParsed{Options: map[string]string{"A": "Astana"}}
	got, err := parseFallbackAnswer(texto, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswer: %v", err)
	}
	if got.Label != "A" || !strings.Contains(got.Reasoning, "{") {
		t.Errorf("resposta = %+v", got)
	}
}

// Bug de produção (ver task-4-report.md, "Hotfix produção"): o agent-go
// respondeu corretamente à questão, mas "answer" veio com o rótulo colado ao
// texto inteiro da alternativa em vez de só a letra. O parse descartava a
// resposta certa como "rótulo desconhecido" e a rota caía no caminho
// degradado com o palpite fraco do Jev — justo na questão em que ele erra.
func TestParseFallbackAnswerRotuloColadoAoTextoDaAlternativa(t *testing.T) {
	// Texto real devolvido pelo agent-go em produção (já como parseFallbackAnswer
	// recebe: texto puro, sem envelope) — o modelo leu '"<rótulo exatamente
	// como listado acima>"' como "a linha inteira" em vez de só a letra.
	texto := `{"answer": "C) Especiação, com divergência entre populações e estabelecimento de isolamento reprodutivo.", "reasoning": "O isolamento geográfico entre populações leva ao acúmulo de diferenças genéticas independentes, podendo resultar em isolamento reprodutivo e especiação."}`
	p := quizParsed{Options: map[string]string{"A": "x", "B": "y", "C": "z", "D": "w"}}
	got, err := parseFallbackAnswer(texto, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswer: %v", err)
	}
	if got.Label != "C" {
		t.Errorf("label = %q, queria %q", got.Label, "C")
	}
}

func TestParseFallbackAnswerRotuloNumericoComTextoColado(t *testing.T) {
	texto := `{"answer":"3) Quatro","reasoning":"x"}`
	p := quizParsed{Options: map[string]string{"1": "um", "2": "dois", "3": "três"}}
	got, err := parseFallbackAnswer(texto, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswer: %v", err)
	}
	if got.Label != "3" {
		t.Errorf("label = %q, queria %q", got.Label, "3")
	}
}

func TestParseFallbackAnswerMinusculaComEspacos(t *testing.T) {
	texto := `{"answer":" c ","reasoning":"x"}`
	p := quizParsed{Options: map[string]string{"A": "x", "B": "y", "C": "z"}}
	got, err := parseFallbackAnswer(texto, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswer: %v", err)
	}
	if got.Label != "C" {
		t.Errorf("label = %q, queria %q", got.Label, "C")
	}
}

func TestParseFallbackAnswerAlternativaComPalavraNaFrente(t *testing.T) {
	texto := `{"answer":"Alternativa B","reasoning":"x"}`
	p := quizParsed{Options: map[string]string{"A": "x", "B": "y"}}
	got, err := parseFallbackAnswer(texto, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswer: %v", err)
	}
	if got.Label != "B" {
		t.Errorf("label = %q, queria %q", got.Label, "B")
	}
}

func TestParseFallbackAnswerRotuloInventadoNaoPassa(t *testing.T) {
	// A tolerância de formato não pode virar invenção de rótulo: "Z" não está
	// no conjunto enviado, e o erro precisa citar o que veio (truncado) pra
	// o próximo diagnóstico ser rápido.
	texto := `{"answer":"Z) qualquer coisa","reasoning":"x"}`
	p := quizParsed{Options: map[string]string{"A": "x", "B": "y", "C": "z", "D": "w"}}
	_, err := parseFallbackAnswer(texto, p)
	if err == nil {
		t.Fatal("queria erro para rótulo fora do conjunto")
	}
	if !strings.Contains(err.Error(), "Z) qualquer coisa") {
		t.Errorf("erro não cita o valor recebido: %v", err)
	}
}
