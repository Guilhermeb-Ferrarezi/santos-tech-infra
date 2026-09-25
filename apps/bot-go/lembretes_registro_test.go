package main

import (
	"os"
	"strings"
	"testing"
)

// Aula marcada em cima da hora não tem lembrete a mandar — as três janelas já
// passaram. Até 25/09 isso também a fazia SUMIR: booking_reminder é o único
// lugar do Postgres que liga aula a telefone e horário, e sem linha a aula não
// existia nem para o bot (AulaDaConversa) nem para o acompanhamento pós-aula.
//
// O teste lê o fonte porque o comportamento é uma consulta ao banco, e um teste
// de integração aqui exigiria Postgres. O que ele trava é a decisão: quando
// nenhum lembrete nasce, ALGUMA linha precisa nascer.
func TestAulaEmCimaDaHoraNaoSomeDoLivroRazao(t *testing.T) {
	src, err := os.ReadFile("lembretes.go")
	if err != nil {
		t.Fatalf("não consegui ler lembretes.go: %v", err)
	}
	s := semCR(string(src))

	i := strings.Index(s, "func (r *LembreteRepo) Agendar")
	if i < 0 {
		t.Fatal("Agendar sumiu")
	}
	fim := strings.Index(s[i:], "\n}\n")
	if fim < 0 {
		t.Fatal("não achei o fim de Agendar")
	}
	corpo := s[i : i+fim]

	if !strings.Contains(corpo, "if criados == 0") {
		t.Error("Agendar voltou a não gravar nada quando todas as janelas já passaram — " +
			"a aula some do painel e o bot esquece que ela existe")
	}
	if !strings.Contains(corpo, "'dispensado'") {
		t.Error("a linha de registro precisa de um status próprio: 'pendente' faria o " +
			"worker tentar mandar um lembrete que não existe mais")
	}
	if !strings.Contains(corpo, "'registro'") {
		t.Error("a linha de registro precisa de um kind próprio, senão colide no " +
			"UNIQUE (tenant, pagina, kind) com uma das três janelas")
	}
	// 'cancelado' aqui significa AULA cancelada. Se a linha de registro nascesse
	// cancelada, ela seria invisível exatamente para quem ela foi criada.
	if strings.Contains(corpo, "'registro', $7, $7, 'cancelado'") {
		t.Error("a linha de registro está nascendo cancelada — invisível para o painel")
	}
}

// O worker só pode enxergar 'pendente'. Se ele passasse a pegar 'dispensado', a
// pessoa receberia um "sua aula é amanhã" depois da aula ter acontecido.
func TestWorkerNaoPegaALinhaDeRegistro(t *testing.T) {
	src, err := os.ReadFile("lembretes.go")
	if err != nil {
		t.Fatalf("não consegui ler lembretes.go: %v", err)
	}
	s := semCR(string(src))

	i := strings.Index(s, "func (r *LembreteRepo) Vencidos")
	if i < 0 {
		t.Fatal("Vencidos sumiu")
	}
	fim := strings.Index(s[i:], "\n}\n")
	if fim < 0 {
		t.Fatal("não achei o fim de Vencidos")
	}
	corpo := s[i : i+fim]

	if !strings.Contains(corpo, "status = 'pendente'") {
		t.Error("Vencidos deixou de filtrar por status='pendente'; a linha de registro " +
			"vira um lembrete enviado fora de hora")
	}
}
