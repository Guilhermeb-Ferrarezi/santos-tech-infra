package main

import "testing"

func agendaEventoFixture(id, tipo string, dataInicio, horaInicio, horaFim string, pcs int) AgendaEvento {
	return AgendaEvento{
		ID: id, Tipo: tipo, Titulo: tipo, DataInicio: dataInicio,
		HoraInicio: horaInicio, HoraFim: horaFim, ComputadoresUsados: pcs,
		Recorrencia: "nenhuma",
	}
}

func TestParseHoraMinutos(t *testing.T) {
	cases := map[string]int{"00:00": 0, "09:05": 545, "19:30": 1170, "19:30:00": 1170}
	for in, want := range cases {
		got, err := parseHoraMinutos(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != want {
			t.Fatalf("%q: got %d, want %d", in, got, want)
		}
	}
	if _, err := parseHoraMinutos("xx"); err == nil {
		t.Fatal("esperava erro pra hora inválida")
	}
}

func TestResolveOcorrenciasNaoRecorrente(t *testing.T) {
	ev := agendaEventoFixture("1", "avulso", "2026-09-04", "20:00", "22:00", 4)
	start, _ := parseData("2026-09-01")
	end, _ := parseData("2026-09-30")
	ocs, err := resolveOcorrencias(ev, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(ocs) != 1 {
		t.Fatalf("esperava 1 ocorrência, got %d", len(ocs))
	}
	fora, _ := parseData("2026-10-01")
	ocs, err = resolveOcorrencias(ev, fora, fora)
	if err != nil {
		t.Fatal(err)
	}
	if len(ocs) != 0 {
		t.Fatalf("evento fora do range não deveria gerar ocorrência, got %d", len(ocs))
	}
}

func TestResolveOcorrenciasSemanal(t *testing.T) {
	dia := 2 // terça (time.Tuesday == 2)
	fim := "2026-10-27"
	ev := AgendaEvento{
		ID: "turma", Tipo: "aula_turma", DataInicio: "2026-09-01",
		HoraInicio: "19:30", HoraFim: "21:30", ComputadoresUsados: 8,
		Recorrencia: "semanal", DiaSemana: &dia, DataFimRecorrencia: &fim,
	}
	start, _ := parseData("2026-09-01")
	end, _ := parseData("2026-09-30")
	ocs, err := resolveOcorrencias(ev, start, end)
	if err != nil {
		t.Fatal(err)
	}
	// Setembro/2026: terças em 01, 08, 15, 22, 29 — 5 ocorrências
	if len(ocs) != 5 {
		t.Fatalf("esperava 5 ocorrências em setembro, got %d", len(ocs))
	}
	for _, o := range ocs {
		if int(o.Data.Weekday()) != dia {
			t.Fatalf("ocorrência fora do dia da semana esperado: %v", o.Data)
		}
	}
}

func TestCheckConflitosCapacidadeExcedida(t *testing.T) {
	turma := agendaEventoFixture("turma", "aula_turma", "2026-09-04", "19:30", "21:30", 8)
	candidato := agendaEventoFixture("", "mix", "2026-09-04", "20:00", "22:00", 5)
	res, err := checkConflitos(candidato, []AgendaEvento{turma})
	if err != nil {
		t.Fatal(err)
	}
	if !res.CapacidadeExcedida {
		t.Fatal("8+5=13 > 10, deveria exceder capacidade")
	}
	if res.PCsOcupados != 13 {
		t.Fatalf("PCsOcupados: got %d, want 13", res.PCsOcupados)
	}
	if len(res.EventosPolitica) != 1 {
		t.Fatalf("esperava 1 conflito de política, got %d", len(res.EventosPolitica))
	}
}

func TestCheckConflitosPoliticaSemEstourarCapacidade(t *testing.T) {
	turma := agendaEventoFixture("turma", "aula_turma", "2026-09-04", "19:30", "21:30", 4)
	candidato := agendaEventoFixture("", "avulso", "2026-09-04", "20:00", "22:00", 5)
	res, err := checkConflitos(candidato, []AgendaEvento{turma})
	if err != nil {
		t.Fatal(err)
	}
	if res.CapacidadeExcedida {
		t.Fatal("4+5=9 <= 10, não deveria exceder capacidade")
	}
	if len(res.EventosPolitica) != 1 {
		t.Fatal("aula+avulso sobrepostos deveria sinalizar conflito de política mesmo sem estourar PCs")
	}
}

func TestCheckConflitosSemSobreposicao(t *testing.T) {
	turma := agendaEventoFixture("turma", "aula_turma", "2026-09-04", "19:30", "21:30", 8)
	candidato := agendaEventoFixture("", "avulso", "2026-09-04", "10:00", "12:00", 5)
	res, err := checkConflitos(candidato, []AgendaEvento{turma})
	if err != nil {
		t.Fatal(err)
	}
	if res.CapacidadeExcedida || len(res.EventosPolitica) != 0 {
		t.Fatal("horários não sobrepostos não deveriam gerar conflito nenhum")
	}
}

func TestCheckConflitosIgnoraSiMesmoEmUpdate(t *testing.T) {
	existente := agendaEventoFixture("evt-1", "avulso", "2026-09-04", "20:00", "22:00", 5)
	candidato := existente // editando o próprio evento, sem mudar nada
	res, err := checkConflitos(candidato, []AgendaEvento{existente})
	if err != nil {
		t.Fatal(err)
	}
	if res.CapacidadeExcedida || len(res.EventosPolitica) != 0 {
		t.Fatal("evento não deveria conflitar consigo mesmo numa edição")
	}
}

func TestCheckConflitosDiaInteiroCandidatoNaoConflita(t *testing.T) {
	turma := agendaEventoFixture("turma", "aula_turma", "2026-09-04", "19:30", "21:30", 8)
	// mesmo dia, "horário" sentinela (00:00-23:59) que sobreporia qualquer coisa
	// se dia_inteiro não fosse excluído do motor de conflito por completo.
	candidato := agendaEventoFixture("", "dia_inteiro", "2026-09-04", "00:00", "23:59", 0)
	res, err := checkConflitos(candidato, []AgendaEvento{turma})
	if err != nil {
		t.Fatal(err)
	}
	if res.CapacidadeExcedida || res.PCsOcupados != 0 || len(res.EventosPolitica) != 0 || len(res.EventosCapacidade) != 0 {
		t.Fatal("evento dia_inteiro não deveria gerar conflito nenhum, mesmo sobrepondo um evento existente")
	}
}

func TestCheckConflitosDiaInteiroExistenteNaoContaCapacidade(t *testing.T) {
	banner := agendaEventoFixture("banner", "dia_inteiro", "2026-09-04", "00:00", "23:59", 0)
	candidato := agendaEventoFixture("", "avulso", "2026-09-04", "20:00", "22:00", 8)
	res, err := checkConflitos(candidato, []AgendaEvento{banner})
	if err != nil {
		t.Fatal(err)
	}
	if res.CapacidadeExcedida || res.PCsOcupados != 8 || len(res.EventosPolitica) != 0 {
		t.Fatal("banner dia_inteiro existente não deveria contar PCs nem gerar conflito de política pra outro evento")
	}
}
