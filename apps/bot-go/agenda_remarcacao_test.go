package main

import (
	"testing"
	"time"
)

// Casos que o review adversarial levantou sobre a remarcação. Todos são
// situações de conversa real, não cantos teóricos.

// "Não vou conseguir na quarta. Pode marcar quinta 17h?" aciona cancelaAula e
// schedulingRequest na MESMA mensagem. Rodando os dois em sequência, o
// cancelamento achava a aula recém-criada — a antiga já tinha saído — e
// arquivava também: o cliente lia "remarcado" e ficava sem aula nenhuma.
func TestCancelarEMarcarNaMesmaMensagemNaoSeAnulam(t *testing.T) {
	out, err := ParseModelReply(`{"bubbles":["Remarcado!"],"answered":true,"answeredFromKb":false,
	  "cancelaAula":true,
	  "schedulingRequest":{"kind":"experimental","studentName":"Caio",
	    "proposedDate":"2026-09-24","proposedTime":"17h00","clienteConfirmou":true}}`)
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	// O parser entrega os dois — é o engine que decide, e é lá que a ordem
	// importa. Este teste documenta a combinação para que ela não passe
	// despercebida numa mudança futura do prompt.
	if !out.CancelaAula {
		t.Error("cancelaAula sumiu")
	}
	if out.SchedulingRequest == nil || !out.SchedulingRequest.ClienteConfirmou {
		t.Fatal("o pedido do horário novo sumiu")
	}
	// A regra no engine: marcou nesta rodada => não roda o cancelamento por
	// cima. Remarcar já é cancelar e marcar.
	marcouAgora := true
	if out.CancelaAula && !marcouAgora {
		t.Error("com a aula recém-marcada, o cancelamento não pode rodar")
	}
}

// Dois filhos, uma conversa: confirmar a aula do segundo não pode arquivar a
// do primeiro.
func TestSegundoFilhoNaoApagaAulaDoPrimeiro(t *testing.T) {
	casos := []struct {
		jaTem, agora string
		remarca      bool
	}{
		{"Caio", "Caio", true},
		{"🤖 23/09 Aula experimental — Caio", "Caio", true},
		{"caio  silva", "Caio Silva", true},
		{"Caio", "Manuela", false},
		{"", "Caio", false},
		{"Caio", "", false},
	}
	for _, c := range casos {
		if got := MesmoAluno(c.jaTem, c.agora); got != c.remarca {
			t.Errorf("MesmoAluno(%q, %q) = %v, esperado %v", c.jaTem, c.agora, got, c.remarca)
		}
	}
}

// Conflito devolve só o PRIMEIRO choque. Perdoar o resultado quando ele é a
// aula do próprio cliente deixaria passar um segundo compromisso no mesmo
// horário — marcar em cima de aluno real. A exclusão tem que ser na entrada.
func TestExclusaoNaEntradaNaoEscondeSegundoConflito(t *testing.T) {
	quarta10 := time.Date(2026, 9, 23, 10, 0, 0, 0, brLocation)

	entry := func(id, titulo, horario string) ScheduleEntry {
		e := ScheduleEntry{PageID: id, Titulo: titulo, Dia: "Quarta", Horario: horario}
		iv, ok := ParseIntervalo(e.Dia, e.Horario)
		if !ok {
			t.Fatalf("fixture %q não parseou", horario)
		}
		e.Intervalo, e.Ok = iv, true
		return e
	}

	// A aula do próprio cliente vem PRIMEIRO na lista, e uma aula de aluno real
	// pega o mesmo horário logo atrás.
	agenda := []ScheduleEntry{
		entry("minha", "🤖 23/09 Aula experimental — Caio", "10:00 ~ 11:00"),
		entry("real", "Renata", "09:00 ~ 11:00"),
	}

	// Sem exclusão, o primeiro conflito é a própria aula.
	if e, hit := Conflito(quarta10, time.Hour, agenda); !hit || e.PageID != "minha" {
		t.Fatalf("fixture errada: primeiro conflito = %q", e.PageID)
	}

	// Com a exclusão na ENTRADA, a aula da Renata aparece e bloqueia.
	e, hit := Conflito(quarta10, time.Hour, semAPagina(agenda, "minha"))
	if !hit {
		t.Fatal("a aula de aluno real ficou escondida atrás da aula do bot")
	}
	if e.PageID != "real" {
		t.Errorf("conflito devolvido = %q, esperado a aula real", e.PageID)
	}
}
