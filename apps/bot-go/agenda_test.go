package main

import (
	"testing"
	"time"
)

// A regra mais importante do arquivo: o bot só mexe no que ele mesmo criou.
// Henrique e Rodrigo lançam aulas na mão na mesma base.
func TestEhAulaExperimental(t *testing.T) {
	dele := []string{
		"Aula experimental — Guilherme",
		"Aula Experimental — Maria Clara",
		"AULA EXPERIMENTAL — joão",
		"  aula experimental — Ana  ",
		"Aula experimental",
	}
	deles := []string{
		"Guilherme",                   // formato antigo, criado antes do marcador
		"Reunião de pais",             //
		"Manutenção dos PCs",          //
		"Turma Tecnologia Júnior",     //
		"Experimental — Pedro",        // sem "Aula": não conta
		"Reagendar aula experimental", // o marcador tem que estar no COMEÇO
		"",                            //
	}
	for _, s := range dele {
		if !EhAulaExperimental(s) {
			t.Errorf("%q deveria ser reconhecido como agendamento do bot", s)
		}
	}
	for _, s := range deles {
		if EhAulaExperimental(s) {
			t.Errorf("%q NÃO é do bot — ele não pode mexer", s)
		}
	}
}

func TestTituloAgendamento(t *testing.T) {
	if got := TituloAgendamento("Guilherme"); got != "Aula experimental — Guilherme" {
		t.Errorf("título = %q", got)
	}
	// Sem nome ainda tem que sair marcado, senão vira uma linha que o bot não
	// consegue mais reconhecer como sua.
	if got := TituloAgendamento("  "); !EhAulaExperimental(got) {
		t.Errorf("título sem aluno perdeu o marcador: %q", got)
	}
}

func TestParseFuncionamento(t *testing.T) {
	j, err := ParseFuncionamento("08:00", "22:00")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if j.AbreMin != 480 || j.FechaMin != 1320 {
		t.Errorf("abre=%d fecha=%d", j.AbreMin, j.FechaMin)
	}
	// Erro em vez de default silencioso: horário errado só aparece quando a
	// família chega e encontra a porta fechada.
	for _, c := range [][2]string{{"", "22:00"}, {"08:00", "xx"}, {"22:00", "08:00"}, {"25:00", "26:00"}} {
		if _, err := ParseFuncionamento(c[0], c[1]); err == nil {
			t.Errorf("ParseFuncionamento(%q,%q) deveria falhar", c[0], c[1])
		}
	}
}

func TestDentroDoFuncionamentoValidaOFim(t *testing.T) {
	j, _ := ParseFuncionamento("08:00", "22:00")
	dia := time.Date(2026, 9, 22, 0, 0, 0, 0, brLocation)
	hora := func(h, m int) time.Time { return dia.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }

	if !j.DentroDoFuncionamento(hora(8, 0), time.Hour) {
		t.Error("8h com aula de 1h deveria caber")
	}
	if !j.DentroDoFuncionamento(hora(21, 0), time.Hour) {
		t.Error("21h–22h deveria caber exatamente")
	}
	// O caso que motiva validar o fim: cabe o começo, não cabe a aula.
	if j.DentroDoFuncionamento(hora(21, 30), time.Hour) {
		t.Error("21h30 + 1h passa das 22h — meia hora de aula com a escola fechada")
	}
	if j.DentroDoFuncionamento(hora(7, 30), time.Hour) {
		t.Error("antes de abrir não pode")
	}
}

// A grade da escola guarda dia da semana e intervalo em texto, não data.
func TestConflitoDetectaSobreposicao(t *testing.T) {
	iv, _ := ParseIntervalo("Quarta", "19:00 ~ 20:00")
	agenda := []ScheduleEntry{{Titulo: "Jackson", Dia: "Quarta", Horario: "19:00 ~ 20:00", Intervalo: iv, Ok: true}}
	// 23/09/2026 é quarta-feira.
	hora := func(h, m int) time.Time { return time.Date(2026, 9, 23, h, m, 0, 0, brLocation) }

	casos := []struct {
		nome     string
		inicio   time.Time
		conflita bool
	}{
		{"mesmo horário", hora(19, 0), true},
		// O que o código antigo não pegava: tratava cada aula como um instante.
		{"começa no meio da outra", hora(19, 30), true},
		{"termina dentro da outra", hora(18, 30), true},
		{"logo antes, sem encostar", hora(18, 0), false},
		{"logo depois, sem encostar", hora(20, 0), false},
	}
	for _, c := range casos {
		_, bateu := Conflito(c.inicio, time.Hour, agenda)
		if bateu != c.conflita {
			t.Errorf("%s: conflito=%v, queria %v", c.nome, bateu, c.conflita)
		}
	}

	// Linha cujo horário não deu para interpretar NÃO bloqueia: o código não
	// finge que entendeu um texto que não entendeu.
	ilegivel := []ScheduleEntry{{Titulo: "X", Dia: "Quarta", Horario: "de manhã", Ok: false}}
	if _, bateu := Conflito(hora(9, 0), time.Hour, ilegivel); bateu {
		t.Error("horário ilegível não deveria bloquear")
	}
}
func TestPodeMarcarReuneAsTravas(t *testing.T) {
	j, _ := ParseFuncionamento("08:00", "22:00")
	agora := time.Date(2026, 9, 22, 10, 0, 0, 0, brLocation) // 22/09/2026 é terça
	dia := time.Date(2026, 9, 22, 0, 0, 0, 0, brLocation)
	hora := func(h, m int) time.Time { return dia.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	iv, _ := ParseIntervalo("Terça", "19:00 ~ 20:00")
	agenda := []ScheduleEntry{{Dia: "Terça", Horario: "19:00 ~ 20:00", Intervalo: iv, Ok: true}}

	casos := []struct {
		nome   string
		quando time.Time
		motivo MotivoRecusa
	}{
		{"livre e com antecedência", hora(15, 0), ""},
		{"no passado", hora(9, 0), RecusaPassado},
		{"daqui a pouco demais", hora(11, 0), RecusaSemAntecedes},
		{"depois de fechar", hora(21, 30), RecusaForaHorario},
		{"em cima de outra aula", hora(19, 0), RecusaConflito},
	}
	for _, c := range casos {
		if got := PodeMarcar(c.quando, time.Hour, agora, j, agenda); got != c.motivo {
			t.Errorf("%s: motivo=%q, queria %q", c.nome, got, c.motivo)
		}
	}
}
