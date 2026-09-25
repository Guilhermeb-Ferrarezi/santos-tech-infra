package main

import (
	"os"
	"strings"
	"testing"
)

// A base de conhecimento vai INTEIRA no prompt de toda mensagem. Ficha inútil
// não é só desorganização: é custo por atendimento e fila de revisão que
// ninguém vai fazer.
//
// Em um dia entraram nove fichas automáticas, e as que motivaram este filtro
// foram: "Interação sem conteúdo útil", "Conteúdo insuficiente para extração",
// "Incomplete inquiry - clarification needed" e "Aula Experimental de
// Testenelson" — esta última, dado de uma conversa virando fato sobre a escola.
func TestFichaImprestavelPegaOLixoQueEntrouDeVerdade(t *testing.T) {
	longo := strings.Repeat("texto de preenchimento para passar do tamanho mínimo. ", 3)

	recusadas := []struct {
		nome string
		e    KBEntry
	}{
		{"anuncia a própria inutilidade",
			KBEntry{Title: "Interação sem conteúdo útil", Content: longo}},
		{"conteúdo insuficiente",
			KBEntry{Title: "Conteúdo insuficiente para extração", Content: longo}},
		{"clarification needed",
			KBEntry{Title: "Incomplete inquiry - clarification needed", Content: longo}},
		{"aula de uma pessoa",
			KBEntry{Title: "Aula Experimental de Testenelson", Content: longo}},
		{"reagendamento de alguém",
			KBEntry{Title: "Reagendamento de aula experimental", Content: longo}},
		{"saudação",
			KBEntry{Title: "Saudação inicial e triagem", Content: longo}},
		{"telefone de cliente no meio",
			KBEntry{Title: "Contato", Content: "O responsável pode ser chamado no 16991590787 para tratar do assunto e combinar."}},
		{"curto demais para ser fato",
			KBEntry{Title: "Horário", Content: "Das 8h às 22h."}},
	}
	for _, c := range recusadas {
		if motivo := fichaImprestavel(c.e); motivo == "" {
			t.Errorf("%s: deveria ter sido recusada, passou", c.nome)
		}
	}

	// E o que é conhecimento de verdade precisa passar — filtro que recusa tudo
	// é tão ruim quanto filtro nenhum.
	aceitas := []KBEntry{
		{Title: "Informática Create — preço e o que inclui",
			Content: "O Informática Create custa R$ 539,90 por mês, com turma de no máximo 10 alunos e todo o equipamento da escola. Na entrada há matrícula de R$ 299,90."},
		{Title: "Endereço e estacionamento",
			Content: "A escola fica na Av. Nove de Julho, 1992, Jardim América, Ribeirão Preto. Como é avenida, não dá para estacionar na rua, mas há garagem própria."},
	}
	for _, e := range aceitas {
		if motivo := fichaImprestavel(e); motivo != "" {
			t.Errorf("ficha legítima recusada (%s): %q", motivo, e.Title)
		}
	}
}

// O modelo precisa PODER dizer que não há nada aproveitável. Antes ele era
// obrigado a devolver título e conteúdo, e obedecia — inventando ficha.
func TestGeradorDeFichaPodeRecusar(t *testing.T) {
	src, err := os.ReadFile("worker.go")
	if err != nil {
		t.Fatalf("não consegui ler worker.go: %v", err)
	}
	// No Windows o git entrega o arquivo com CRLF. Sem normalizar, procurar
	// "\n}\n" não acha nada e o recorte abaixo estoura o slice — o teste morria
	// de pânico em vez de falhar dizendo o que estava errado.
	s := semCR(string(src))

	i := strings.Index(s, "func (w *Worker) handleKBGap")
	if i < 0 {
		t.Fatal("handleKBGap sumiu")
	}
	fim := strings.Index(s[i:], "\n}\n")
	if fim < 0 {
		t.Fatal("não achei o fim de handleKBGap")
	}
	corpo := s[i : i+fim]

	if !strings.Contains(corpo, `util`) {
		t.Error("o prompt não oferece ao modelo a saída de recusar")
	}
	if !strings.Contains(corpo, "extraida.Util == nil || !*extraida.Util") {
		t.Error("o código não respeita a recusa — sem util:true não pode virar ficha")
	}
	if !strings.Contains(corpo, "fichaImprestavel") {
		t.Error("a peneira final sumiu; a do modelo sozinha já deixou passar lixo antes")
	}
}

// Handoff que não cala o bot não é handoff, é aviso: a coordenação entra na
// conversa e o bot segue falando junto, às vezes contradizendo quem assumiu.
func TestHandoffSilenciaOBot(t *testing.T) {
	src, err := os.ReadFile("engine.go")
	if err != nil {
		t.Fatalf("não consegui ler engine.go: %v", err)
	}
	s := semCR(string(src))

	i := strings.Index(s, "if output.Handoff {")
	if i < 0 {
		t.Fatal("o bloco de handoff sumiu")
	}
	bloco := s[i:min(i+1200, len(s))]
	if !strings.Contains(bloco, "SetBotEnabled") {
		t.Error("o handoff não desliga o bot para a conversa")
	}
	if !strings.Contains(bloco, "false") {
		t.Error("o handoff não está desligando (passou true?)")
	}
}
