package main

import "testing"

func TestNotionParseHorario(t *testing.T) {
	// Os 3 formatos que existem de verdade na base, mais os casos que têm que
	// ser recusados em vez de virar horário torto.
	casos := []struct{ in, ini, fim string }{
		{"18:00–19:00", "18:00", "19:00"}, // travessão
		{"16:00 ~ 18:00", "16:00", "18:00"},
		{"16h00-17h00", "16:00", "17:00"},
		{"19h30-21h30", "19:30", "21:30"},
		{"8:00–9:00", "08:00", "09:00"}, // hora sem zero à esquerda
		{"", "", ""},
		{"das 17 as 19", "", ""}, // sem minutos: não dá pra afirmar 17:00
		{"19:00", "", ""},        // só um horário
		{"20:00–19:00", "", ""},  // fim antes do início
		{"25:00–26:00", "", ""},  // hora inválida
	}
	for _, c := range casos {
		ini, fim, ok := notionParseHorario(c.in)
		if c.ini == "" {
			if ok {
				t.Errorf("%q: devia recusar, veio %s-%s", c.in, ini, fim)
			}
			continue
		}
		if !ok || ini != c.ini || fim != c.fim {
			t.Errorf("%q: veio (%s,%s,%v), queria (%s,%s,true)", c.in, ini, fim, ok, c.ini, c.fim)
		}
	}
}

func TestNotionLimpaNomeESepara(t *testing.T) {
	casos := []struct {
		tag   string
		nome  string
		split []string
	}{
		{"Igor (pai: Hamilton)", "Igor", []string{"Igor"}},
		{"Walisson - Informática", "Walisson", []string{"Walisson"}},
		{"Leo - Particular - Informática Básica", "Leo", []string{"Leo"}},
		{"Paloma Felipe José e João - Informática Básica", "Paloma Felipe José e João", []string{"Paloma Felipe José", "João"}},
		{"Mathias Davi e Theo (TER e QUI) Informática básica", "Mathias Davi e Theo Informática básica", []string{"Mathias Davi", "Theo Informática básica"}},
	}
	for _, c := range casos {
		if got := notionLimpaNome(c.tag); got != c.nome {
			t.Errorf("limpaNome(%q) = %q, queria %q", c.tag, got, c.nome)
		}
		got := notionSeparaAlunos(c.tag)
		if len(got) != len(c.split) {
			t.Errorf("separaAlunos(%q) = %v, queria %v", c.tag, got, c.split)
		}
	}
}

// Linhas reais da base "Agenda de Aulas" (03/09/2026), incluindo as bagunçadas.
func linhasReais() []notionAulaRow {
	return []notionAulaRow{
		{PageID: "p-agenda", Aula: "Agenda"},
		{PageID: "p-clau-qua", Aula: "Claudemir", Aluno: []string{"Walisson - Informática"}, Dia: "Quarta", Horario: "18:00–19:00", Professor: "Henrique", Conteudo: "Informática básica: Word - Power Point - Excel e Power BI"},
		{PageID: "p-igor-seg", Aula: "Igor", Aluno: []string{"Igor (pai: Hamilton)"}, Dia: "Segunda", Horario: "19:00–20:00", Professor: "Rodrigo", Conteudo: "Informática básica Construct 3 e Roblox Studio"},
		{PageID: "p-igor-qui", Aula: "Igor", Aluno: []string{"Igor (pai: Hamilton)"}, Dia: "Quinta", Horario: "19:00–20:00", Professor: "Rodrigo", Conteudo: "Informática básica Construct 3 e Roblox Studio"},
		{PageID: "p-inf-ter", Aula: "Turma Informática", Aluno: []string{"Mathias Davi e Theo (TER e QUI) Informática básica"}, Dia: "Terça", Horario: "16h00-17h00", Professor: "Rodrigo", Conteudo: "Informática básica: Word - Power Point - Excel e Power BI"},
		{PageID: "p-inf-noite", Aula: "Turma Informática", Aluno: []string{"Paloma Felipe José e João - Informática Básica"}, Dia: "Terça", Horario: "19h30-21h30", Professor: "Rodrigo", Conteudo: "Informática básica: Word - Power Point - Excel e Power BI"},
		{PageID: "p-renata", Aula: "Renata", Dia: "Terça", Horario: "10:00 ~ 12:00", Professor: "Rodrigo", Conteudo: "Excel & Pacote Office"},
		{PageID: "p-magnaldo", Aula: "Magnaldo", Aluno: []string{"Magnaldo"}, Dia: "Quinta", Horario: "18:00–19:00", Professor: "Rodrigo"}, // sem Conteúdo
		{PageID: "p-exp", Aula: "05/09 Aula Experimental", Dia: "Sábado", Horario: "10:00 ~ 11:00", Professor: "Rodrigo", Conteudo: "Programação"},
	}
}

func TestPlanNotionAgenda(t *testing.T) {
	itens := planNotionAgenda(linhasReais())
	por := map[string]notionPlanItem{}
	for _, i := range itens {
		por[i.NotionPageID] = i
	}

	// Ignorados: cada um com um motivo diferente e explícito.
	for _, id := range []string{"p-agenda", "p-magnaldo", "p-exp"} {
		if por[id].Action != notionPlanIgnorado {
			t.Errorf("%s devia ser ignorado, veio %q", id, por[id].Action)
		}
		if por[id].Motivo == "" {
			t.Errorf("%s ignorado sem motivo", id)
		}
	}

	// As 3 aulas do Igor são a MESMA turma (mesma chave), em dias diferentes.
	if por["p-igor-seg"].TurmaKey != por["p-igor-qui"].TurmaKey {
		t.Error("as aulas do Igor deviam cair na mesma turma")
	}
	if por["p-igor-seg"].DayOfWeek != 1 || por["p-igor-qui"].DayOfWeek != 4 {
		t.Errorf("dias errados: seg=%d qui=%d", por["p-igor-seg"].DayOfWeek, por["p-igor-qui"].DayOfWeek)
	}

	// Duas linhas com o MESMO título "Turma Informática" mas grupos de alunos
	// diferentes têm que virar turmas separadas — juntar misturaria os alunos.
	if por["p-inf-ter"].TurmaKey == por["p-inf-noite"].TurmaKey {
		t.Error("grupos de alunos diferentes viraram a mesma turma")
	}
	if por["p-inf-noite"].StartTime != "19:30" || por["p-inf-noite"].EndTime != "21:30" {
		t.Errorf("horário da turma da noite: %s-%s", por["p-inf-noite"].StartTime, por["p-inf-noite"].EndTime)
	}

	// Título "Claudemir" com aluno "Walisson": a turma segue a TAG, não o título.
	if got := por["p-clau-qua"].Turma; got != "Walisson" {
		t.Errorf("turma do Claudemir/Walisson = %q, queria Walisson", got)
	}

	// Linha sem tag: o nome vem do título.
	if por["p-renata"].Turma != "Renata" || por["p-renata"].Action != notionPlanCriar {
		t.Errorf("Renata: turma=%q action=%q", por["p-renata"].Turma, por["p-renata"].Action)
	}
}

// A classificação decide em qual tela a linha vai aparecer (Aulas particulares
// x Turmas), então precisa estar travada — inclusive nos casos que a heurística
// de separar nomes erra sozinha.
func TestPlanNotionClassificaParticular(t *testing.T) {
	rows := []notionAulaRow{
		// 1 aluno → aula particular
		{PageID: "a", Aula: "Igor", Aluno: []string{"Igor (pai: Hamilton)"}, Dia: "Segunda", Horario: "19:00–20:00", Conteudo: "Roblox"},
		{PageID: "b", Aula: "Renata", Dia: "Terça", Horario: "10:00 ~ 12:00", Conteudo: "Excel"}, // nome no título
		{PageID: "c", Aula: "Leonardo", Aluno: []string{"Leo - Particular - Informática Básica"}, Dia: "Sábado", Horario: "15h00-16h00", Conteudo: "Excel"},
		// vários alunos → turma
		{PageID: "d", Aula: "Turma Programação", Aluno: []string{"Ana Nicolas e Ruan - Html Css e Javascript"}, Dia: "Sábado", Horario: "13h00-15h00", Conteudo: "HTML"},
		{PageID: "e", Aula: "Turma Informática", Aluno: []string{"Paloma Felipe José e João - Informática Básica"}, Dia: "Terça", Horario: "19h30-21h30", Conteudo: "Info"},
		// título "Turma" com tag de nome único: o título explícito vence a heurística
		{PageID: "f", Aula: "Turma Excel", Aluno: []string{"Fulano"}, Dia: "Quarta", Horario: "10:00–11:00", Conteudo: "Excel"},
	}
	quer := map[string]bool{"a": true, "b": true, "c": true, "d": false, "e": false, "f": false}
	for _, p := range planNotionAgenda(rows) {
		if p.Action != notionPlanCriar {
			t.Fatalf("%s deveria ser criada, veio %q (%s)", p.NotionPageID, p.Action, p.Motivo)
		}
		if p.Individual != quer[p.NotionPageID] {
			t.Errorf("%s (%s): individual=%v, queria %v", p.NotionPageID, p.Turma, p.Individual, quer[p.NotionPageID])
		}
	}
}
