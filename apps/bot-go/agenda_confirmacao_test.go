package main

import (
	"strings"
	"testing"
	"time"
)

// O bug: propor um horário e marcá-lo eram a mesma coisa.
//
// Numa conversa real o bot ofereceu quarta 10h e gravou. Na mensagem seguinte
// leu a própria aula como ocupada, disse "me corrigindo, às 10h também já está
// preenchido", ofereceu 11h — e gravou de novo. Depois quinta 17h, mesma coisa.
// Três aulas fantasma, nenhuma pedida pelo cliente, e ele terminou a conversa
// sem horário nenhum.
//
// Estes testes travam as duas pontas: o modelo não deve mandar o pedido antes
// do aceite, e o código não deve gravar mesmo que ele mande.

func TestParserExigeConfirmacaoDoCliente(t *testing.T) {
	base := `{"bubbles":["ok"],"answered":true,"answeredFromKb":false,
	  "schedulingRequest":{"kind":"experimental","studentName":"Caio",
	  "proposedDate":"2026-09-24","proposedTime":"17h00"%s}}`

	casos := []struct {
		nome     string
		extra    string
		confirma bool
	}{
		{"campo ausente — modelo antigo ou prompt velho", "", false},
		{"modelo diz que ainda não aceitou", `,"clienteConfirmou":false`, false},
		{"cliente aceitou", `,"clienteConfirmou":true`, true},
		{"tipo errado não vira confirmação", `,"clienteConfirmou":"sim"`, false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			out, err := ParseModelReply(strings.Replace(base, "%s", c.extra, 1))
			if err != nil {
				// JSON malformado no campo derruba o schedulingRequest inteiro,
				// o que também é "não marca" — o lado seguro.
				if c.confirma {
					t.Fatalf("erro inesperado: %v", err)
				}
				return
			}
			if out.SchedulingRequest == nil {
				if c.confirma {
					t.Fatal("schedulingRequest sumiu")
				}
				return
			}
			if got := out.SchedulingRequest.ClienteConfirmou; got != c.confirma {
				t.Errorf("clienteConfirmou = %v, esperado %v", got, c.confirma)
			}
		})
	}
}

func TestPromptProibeMarcarAntesDoAceite(t *testing.T) {
	cfg := TenantConfig{
		EscolaAbre: "08:00", EscolaFecha: "22:00", AulaDuracaoMin: 60,
		AgendaAutoConfirm: true,
	}
	p := BuildPrompt(cfg, ConversationContext{}, "amanhã às 10h dá?",
		time.Unix(1789000000, 0).UTC())

	if !strings.Contains(p, `"clienteConfirmou"`) {
		t.Error("o schema do prompt não pede clienteConfirmou")
	}
	if !strings.Contains(p, "Propor um horário não é marcar") {
		t.Error("o prompt não separa propor de marcar — é a regra que faltava")
	}
	// A instrução antiga mandava preencher junto com a proposta. Se ela voltar,
	// o bug volta inteiro.
	if strings.Contains(p, "você já tiver um horário proposto") {
		t.Error("a instrução antiga (preencher ao propor) voltou ao prompt")
	}
}

func TestSemAPaginaTiraSoAQueFoiPedida(t *testing.T) {
	agenda := []ScheduleEntry{
		{PageID: "a", Titulo: "Renata", Dia: "Quarta", Horario: "08:00 ~ 10:00"},
		{PageID: "b", Titulo: "🤖 23/09 Aula experimental — Caio", Dia: "Quarta", Horario: "10:00 ~ 11:00"},
		{PageID: "c", Titulo: "Jackson", Dia: "Quarta", Horario: "19:00 ~ 20:00"},
	}

	out := semAPagina(agenda, "b")
	if len(out) != 2 {
		t.Fatalf("sobrou %d linha(s), esperado 2", len(out))
	}
	for _, e := range out {
		if e.PageID == "b" {
			t.Error("a página pedida continua na agenda")
		}
	}

	// Sem id, nada sai — remarcação de quem não tinha aula não pode abrir
	// buraco nenhum na grade.
	if len(semAPagina(agenda, "")) != 3 {
		t.Error("id vazio não pode remover nada")
	}
	if len(semAPagina(agenda, "nao-existe")) != 3 {
		t.Error("id desconhecido não pode remover nada")
	}
}

// Remarcar é mover, não acumular: a aula nova ocupa o horário novo e o antigo
// volta a ficar livre para outra família.
func TestRemarcarLiberaOHorarioAntigo(t *testing.T) {
	quarta10 := time.Date(2026, 9, 23, 10, 0, 0, 0, brLocation)
	quinta17 := time.Date(2026, 9, 24, 17, 0, 0, 0, brLocation)

	antiga := ScheduleEntry{PageID: "b", Titulo: "🤖 23/09 Aula experimental — Caio",
		Dia: "Quarta", Horario: "10:00 ~ 11:00"}
	iv, ok := ParseIntervalo(antiga.Dia, antiga.Horario)
	if !ok {
		t.Fatal("fixture não parseou")
	}
	antiga.Intervalo, antiga.Ok = iv, true
	agenda := []ScheduleEntry{antiga}

	// Com a aula antiga na conta, o próprio horário dela aparece ocupado.
	if _, hit := Conflito(quarta10, time.Hour, agenda); !hit {
		t.Error("a aula antiga deveria ocupar o próprio horário")
	}
	// Tirada da conta, o horário volta a ficar livre — é o que permite ao
	// cliente remarcar para o mesmo horário depois de desistir.
	if _, hit := Conflito(quarta10, time.Hour, semAPagina(agenda, "b")); hit {
		t.Error("sem a aula antiga, quarta 10h tinha que estar livre")
	}
	// E o horário novo nunca esteve ocupado por ela.
	if _, hit := Conflito(quinta17, time.Hour, agenda); hit {
		t.Error("quinta 17h não tem relação com a aula de quarta")
	}
}
