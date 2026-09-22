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

// ── modo múltipla resposta ──────────────────────────────────────────────

func TestBuildFallbackPromptMultiplaPedeTodasAsCorretas(t *testing.T) {
	p := quizParsed{
		Question: "Assinale as corretas.",
		Options:  map[string]string{"A": "primeira", "B": "segunda", "C": "terceira"},
		Order:    []string{"A", "B", "C"},
	}
	got := buildFallbackPromptMultipla(p, false)
	if !strings.Contains(got, "Assinale as corretas.") {
		t.Error("prompt sem o enunciado")
	}
	if !strings.Contains(strings.ToUpper(got), "MÚLTIPLA") || !strings.Contains(got, "TODAS") {
		t.Errorf("prompt não deixa claro que é pra marcar todas as corretas:\n%s", got)
	}
	if !strings.Contains(got, `"answers"`) {
		t.Error("prompt não pede o campo \"answers\" (lista) — o parse depende disso")
	}
}

func TestParseFallbackAnswerMultiplaListaSimples(t *testing.T) {
	texto := `{"answers":["A","C"],"reasoning":"porque sim"}`
	p := quizParsed{Options: map[string]string{"A": "x", "B": "y", "C": "z"}, Order: []string{"A", "B", "C"}}
	got, err := parseFallbackAnswerMultipla(texto, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswerMultipla: %v", err)
	}
	if want := []string{"A", "C"}; len(got.Labels) != 2 || got.Labels[0] != want[0] || got.Labels[1] != want[1] {
		t.Errorf("labels = %v, queria %v", got.Labels, want)
	}
}

// Rótulo inválido junto de válidos: mantém os válidos, não descarta a
// resposta inteira (regra explícita da spec — diferente do modo único).
func TestParseFallbackAnswerMultiplaRotuloInvalidoJuntoDeValidosMantemOsValidos(t *testing.T) {
	texto := `{"answers":["A","Z","C"],"reasoning":"x"}`
	p := quizParsed{Options: map[string]string{"A": "x", "B": "y", "C": "z"}, Order: []string{"A", "B", "C"}}
	got, err := parseFallbackAnswerMultipla(texto, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswerMultipla: %v", err)
	}
	if want := []string{"A", "C"}; len(got.Labels) != 2 || got.Labels[0] != want[0] || got.Labels[1] != want[1] {
		t.Errorf("labels = %v, queria %v (Z descartado, A e C mantidos)", got.Labels, want)
	}
}

// Modelo devolve "answer" string (formato do modo único) em vez de
// "answers" lista — aceita como lista de um elemento.
func TestParseFallbackAnswerMultiplaAnswerStringViraListaDeUm(t *testing.T) {
	texto := `{"answer":"B","reasoning":"só essa"}`
	p := quizParsed{Options: map[string]string{"A": "x", "B": "y", "C": "z"}, Order: []string{"A", "B", "C"}}
	got, err := parseFallbackAnswerMultipla(texto, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswerMultipla: %v", err)
	}
	if want := []string{"B"}; len(got.Labels) != 1 || got.Labels[0] != want[0] {
		t.Errorf("labels = %v, queria %v", got.Labels, want)
	}
}

func TestParseFallbackAnswerMultiplaTodosInvalidosDaErro(t *testing.T) {
	texto := `{"answers":["Y","Z"],"reasoning":"x"}`
	p := quizParsed{Options: map[string]string{"A": "x", "B": "y"}, Order: []string{"A", "B"}}
	if _, err := parseFallbackAnswerMultipla(texto, p); err == nil {
		t.Error("queria erro: nenhum rótulo do conjunto enviado")
	}
}

func TestParseFallbackAnswerMultiplaReordenaPorPOrder(t *testing.T) {
	// O modelo escreveu C antes de A — a resposta final segue p.Order, não a
	// ordem em que o modelo escreveu.
	texto := `{"answers":["C","A"],"reasoning":"x"}`
	p := quizParsed{Options: map[string]string{"A": "x", "B": "y", "C": "z"}, Order: []string{"A", "B", "C"}}
	got, err := parseFallbackAnswerMultipla(texto, p)
	if err != nil {
		t.Fatalf("parseFallbackAnswerMultipla: %v", err)
	}
	if want := []string{"A", "C"}; len(got.Labels) != 2 || got.Labels[0] != want[0] || got.Labels[1] != want[1] {
		t.Errorf("labels = %v, queria %v (na ordem de p.Order)", got.Labels, want)
	}
}

// ── modo aberto ──────────────────────────────────────────────────────────

func TestBuildOpenPromptMencionaLacunasEJSON(t *testing.T) {
	got := buildOpenPrompt("Um cartaz instrucional tem como finalidade ___ o público", false)
	if !strings.Contains(got, "Um cartaz instrucional tem como finalidade ___ o público") {
		t.Error("prompt sem o texto da questão")
	}
	if !strings.Contains(got, "JSON") {
		t.Error("prompt não pede JSON — o parse depende disso")
	}
	if !strings.Contains(strings.ToLower(got), "lacuna") {
		t.Error("prompt não instrui sobre questões de preencher lacuna")
	}
	if strings.Contains(strings.ToLower(got), "imagem anexad") {
		t.Error("prompt sem imagem não deveria mencionar imagem anexada")
	}
}

func TestBuildOpenPromptComImagemMencionaAFigura(t *testing.T) {
	got := buildOpenPrompt("O que a imagem mostra?", true)
	if !strings.Contains(strings.ToLower(got), "imagem") {
		t.Errorf("prompt com imagem não menciona a figura anexada:\n%s", got)
	}
}

// TestBuildOpenPromptInstruiPreferirTermosDoTexto cobre o caso de produção:
// questão de completar lacunas com gabarito "orientar / procedimentos /
// regras", mas a rota respondeu com sinônimos semanticamente corretos
// ("informar; regulamentos; normas") que não batem com o texto de apoio — e
// numa correção objetiva de lacunas o sinônimo costuma não valer ponto. O
// prompt precisa instruir o modelo a preferir a palavra que já está no
// texto, só recorrendo a conhecimento externo quando o texto não oferece a
// resposta.
func TestBuildOpenPromptInstruiPreferirTermosDoTexto(t *testing.T) {
	got := buildOpenPrompt("Um cartaz instrucional tem como finalidade ___ o público", false)
	baixo := strings.ToLower(got)
	if !strings.Contains(baixo, "prefira") && !strings.Contains(baixo, "preferir") {
		t.Errorf("prompt não instrui a preferir os termos do próprio texto:\n%s", got)
	}
	if !strings.Contains(baixo, "sinônimo") {
		t.Errorf("prompt não menciona explicitamente evitar sinônimo:\n%s", got)
	}
	if !strings.Contains(baixo, "conhecimento externo") {
		t.Errorf("prompt não delimita quando usar conhecimento externo (só quando o texto não oferecer a resposta):\n%s", got)
	}
}

func TestParseOpenAnswerJSONSimples(t *testing.T) {
	texto := `{"answer":"instruir; comportamentos; procedimentos","reasoning":"literal do enunciado"}`
	got, err := parseOpenAnswer(texto)
	if err != nil {
		t.Fatalf("parseOpenAnswer: %v", err)
	}
	if got.Answer != "instruir; comportamentos; procedimentos" || got.Reasoning == "" {
		t.Errorf("resposta = %+v", got)
	}
}

func TestParseOpenAnswerJSONEmbrulhadoEmTexto(t *testing.T) {
	texto := "Claro! Aqui está:\n```json\n{\"answer\": \"instruir; comportamentos; procedimentos\", \"reasoning\": \"literal\"}\n```"
	got, err := parseOpenAnswer(texto)
	if err != nil {
		t.Fatalf("parseOpenAnswer: %v", err)
	}
	if got.Answer != "instruir; comportamentos; procedimentos" {
		t.Errorf("answer = %q", got.Answer)
	}
}

func TestParseOpenAnswerSemJSONUsaTextoCru(t *testing.T) {
	// Diferente do modo múltipla (onde um rótulo inválido é inútil), no modo
	// aberto texto solto ainda é uma resposta aproveitável.
	texto := "A resposta é instruir o público sobre os procedimentos que devem ser seguidos."
	got, err := parseOpenAnswer(texto)
	if err != nil {
		t.Fatalf("parseOpenAnswer: %v", err)
	}
	if got.Answer != texto {
		t.Errorf("answer = %q, queria o texto cru como resposta", got.Answer)
	}
}

func TestParseOpenAnswerJSONSemCampoAnswerUsaTextoCru(t *testing.T) {
	texto := `{"reasoning":"esqueci o answer"}`
	got, err := parseOpenAnswer(texto)
	if err != nil {
		t.Fatalf("parseOpenAnswer: %v", err)
	}
	if got.Answer != texto {
		t.Errorf("answer = %q, queria o texto cru (JSON sem \"answer\" útil)", got.Answer)
	}
}

func TestParseOpenAnswerTextoVazioErro(t *testing.T) {
	if _, err := parseOpenAnswer(""); err == nil {
		t.Error("queria erro para texto vazio")
	}
	if _, err := parseOpenAnswer("   "); err == nil {
		t.Error("queria erro para texto só com espaço")
	}
}

func TestParseOpenAnswerTruncaRespostaLonga(t *testing.T) {
	longo := strings.Repeat("a", quizOpenAnswerMaxChars+50)
	texto := `{"answer":"` + longo + `","reasoning":"x"}`
	got, err := parseOpenAnswer(texto)
	if err != nil {
		t.Fatalf("parseOpenAnswer: %v", err)
	}
	r := []rune(got.Answer)
	if len(r) != quizOpenAnswerMaxChars+1 { // +1 pra reticências
		t.Errorf("len(answer) = %d, queria %d (%d chars + reticências)", len(r), quizOpenAnswerMaxChars+1, quizOpenAnswerMaxChars)
	}
	if !strings.HasSuffix(got.Answer, "…") {
		t.Errorf("resposta truncada sem reticências: %q", got.Answer)
	}
}

func TestParseOpenAnswerRespostaCurtaNaoTrunca(t *testing.T) {
	texto := `{"answer":"resposta curta","reasoning":"x"}`
	got, err := parseOpenAnswer(texto)
	if err != nil {
		t.Fatalf("parseOpenAnswer: %v", err)
	}
	if got.Answer != "resposta curta" {
		t.Errorf("answer = %q, não deveria truncar resposta curta", got.Answer)
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
