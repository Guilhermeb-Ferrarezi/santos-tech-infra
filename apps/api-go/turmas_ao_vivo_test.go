package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func evSemanal(tipo string, dia int, ini, fim string, pcs *int) AgendaEvento {
	return AgendaEvento{Tipo: tipo, Recorrencia: "semanal", DiaSemana: &dia, DataInicio: "2026-09-01",
		HoraInicio: ini + ":00", HoraFim: fim + ":00", ComputadoresUsados: pcs}
}

func TestJanelasLivres(t *testing.T) {
	hoje := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	dez := 10
	um := 1
	encerrado := "2026-09-01"
	futuro := evSemanal("aula_turma", 2, "08:00", "09:00", &um)
	futuro.DataInicio = "2026-10-06" // turma que ainda vai começar também ocupa
	eventos := []AgendaEvento{
		evSemanal("aula_turma", 6, "13:00", "15:00", &dez),
		evSemanal("aula_particular", 6, "10:00", "12:00", nil), // PCs não informados = cheio
		evSemanal("aula_particular", 6, "15:00", "16:00", &um), // encosta no fim da turma: sem janela no meio
		{Tipo: "mix", Recorrencia: "nenhuma", DataInicio: "2026-10-03", HoraInicio: "08:00:00", HoraFim: "10:00:00", ComputadoresUsados: &dez},
		func() AgendaEvento {
			e := evSemanal("aula_particular", 1, "08:00", "20:00", &um)
			e.DataFimRecorrencia = &encerrado // já acabou: não ocupa mais
			return e
		}(),
		futuro,
	}
	got := janelasLivres(eventos, []int{1, 2, 6}, 8*60, 18*60, 60, hoje)
	want := []JanelaLivre{
		{DiaSemana: 1, HoraInicio: "08:00", HoraFim: "18:00"},
		{DiaSemana: 2, HoraInicio: "09:00", HoraFim: "18:00"},
		// Avulso (Mix) não bloqueia; 12:00–13:00 cabe 60 min; 16:00–18:00 cabe.
		{DiaSemana: 6, HoraInicio: "08:00", HoraFim: "10:00"},
		{DiaSemana: 6, HoraInicio: "12:00", HoraFim: "13:00"},
		{DiaSemana: 6, HoraInicio: "16:00", HoraFim: "18:00"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("janelas:\n got %+v\nwant %+v", got, want)
	}

	// Janela menor que a duração pedida some.
	got = janelasLivres(eventos, []int{6}, 8*60, 18*60, 90, hoje)
	for _, j := range got {
		if j.HoraInicio == "12:00" {
			t.Fatalf("12:00–13:00 não cabe 90 min: %+v", got)
		}
	}
}

func TestParseHorariosLivresParams(t *testing.T) {
	ruins := []string{
		"",
		"abre=08:00&fecha=18:00&duracao=60", // sem dias
		"abre=08:00&fecha=18:00&dias=1",     // sem duração
		"abre=18:00&fecha=08:00&duracao=60&dias=1",               // fecha antes de abrir
		"abre=08:00&fecha=18:00&duracao=10&dias=1",               // duração < 15
		"abre=08:00&fecha=18:00&duracao=481&dias=1",              // duração > 480
		"abre=08:00&fecha=18:00&duracao=60&dias=7",               // dia fora de 0–6
		"abre=08:00&fecha=18:00&duracao=60&dias=1,x",             // lixo
		"abre=8h&fecha=18:00&duracao=60&dias=1",                  // hora inválida
		"abre=08:00&fecha=18:00&duracao=60&dias=1,1,1,1,1,1,1,1", // repetição
	}
	for _, q := range ruins {
		r := httptest.NewRequest("GET", "/agenda/horarios-livres?"+q, nil)
		if _, err := parseHorariosLivresParams(r); err == nil {
			t.Fatalf("%q deveria ser recusado", q)
		}
	}
	r := httptest.NewRequest("GET", "/agenda/horarios-livres?abre=08:00&fecha=22:00&duracao=120&dias=6,1", nil)
	p, err := parseHorariosLivresParams(r)
	if err != nil || p.abre != 480 || p.fecha != 1320 || p.duracao != 120 || !reflect.DeepEqual(p.dias, []int{1, 6}) {
		t.Fatalf("params válidos: %+v %v", p, err)
	}
}

func TestMesesDeAndamento(t *testing.T) {
	d := func(s string) time.Time { x, _ := time.Parse("2006-01-02", s); return x }
	casos := []struct {
		inicio, hoje string
		meses        int
	}{
		{"2026-08-01", "2026-09-25", 1},
		{"2026-06-26", "2026-09-25", 2}, // ainda não completou 3
		{"2026-06-25", "2026-09-25", 3},
		{"2026-10-20", "2026-09-25", 0}, // começa no futuro
		{"2025-09-25", "2026-09-25", 12},
	}
	for _, c := range casos {
		if got := mesesDeAndamento(d(c.inicio), d(c.hoje)); got != c.meses {
			t.Fatalf("%s→%s: got %d want %d", c.inicio, c.hoje, got, c.meses)
		}
	}
}

func TestMontaTurmasAbertas(t *testing.T) {
	hoje := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	d := func(s string) time.Time { x, _ := time.Parse("2006-01-02", s); return x }
	id22, id23 := int64(22), int64(23)
	ev := func(id *int64, dia int, ini, fim string) AgendaEvento {
		e := evSemanal("aula_turma", dia, ini, fim, nil)
		e.PortalClassID = id
		return e
	}
	linhas := []turmaPortalLinha{
		{ID: 22, Nome: "Turma Programação", CursoID: 7, CursoNome: "Programação", Inicio: d("2026-08-01"), Fim: d("2027-08-01"), Capacidade: 10, Alunos: 3},
		{ID: 23, Nome: "Turma Informática", CursoID: 5, CursoNome: "Informática", Inicio: d("2026-03-01"), Fim: d("2027-03-01"), Capacidade: 4, Alunos: 6},
		{ID: 24, Nome: "Sem horário", CursoID: 5, CursoNome: "Informática", Inicio: d("2026-03-01"), Fim: d("2027-03-01"), Capacidade: 10},
	}
	eventos := []AgendaEvento{ev(&id22, 6, "13:00", "15:00"), ev(&id23, 2, "19:30", "21:30"), ev(&id23, 4, "19:30", "21:30")}
	grade := map[int64][]HorarioTurma{
		22: {{DiaSemana: 6, HoraInicio: "13:00", HoraFim: "15:00"}},
		23: {{DiaSemana: 2, HoraInicio: "19:30", HoraFim: "21:30"}}, // Portal só tem terça: diverge
	}
	got := montaTurmasAbertas(linhas, eventos, grade, hoje)
	if len(got) != 2 {
		t.Fatalf("turma sem horário ligado não pode entrar: %+v", got)
	}
	t22, t23 := got[0], got[1]
	if t22.ID != 22 || t22.Vagas != 7 || t22.MesesDeAndamento != 1 || t22.Divergente || t22.FimPrevisto != "2027-08-01" || t22.Inicio != "2026-08-01" {
		t.Fatalf("turma 22: %+v", t22)
	}
	if t23.Vagas != 0 || !t23.Divergente || len(t23.Horarios) != 2 || t23.MesesDeAndamento != 6 {
		t.Fatalf("turma 23 (lotada, divergente): %+v", t23)
	}
	if t23.Horarios[0].DiaSemana != 2 || t23.Horarios[1].DiaSemana != 4 {
		t.Fatalf("horários ordenados por dia: %+v", t23.Horarios)
	}
}

func TestParseTurmasAbertasFiltros(t *testing.T) {
	for _, q := range []string{"curso=x", "curso=0", "diaSemana=7", "diaSemana=-1", "diaSemana=a"} {
		r := httptest.NewRequest("GET", "/portal/turmas-abertas?"+q, nil)
		if _, err := parseTurmasAbertasFiltros(r); err == nil {
			t.Fatalf("%q deveria ser 400", q)
		}
	}
	r := httptest.NewRequest("GET", "/portal/turmas-abertas?curso=7&diaSemana=6", nil)
	f, err := parseTurmasAbertasFiltros(r)
	if err != nil || f.curso == nil || *f.curso != 7 || f.diaSemana == nil || *f.diaSemana != 6 {
		t.Fatalf("filtros válidos: %+v %v", f, err)
	}
}

func TestPermGuardTurmasAoVivoNoToken(t *testing.T) {
	s := testServer(Config{})
	for _, h := range []http.HandlerFunc{
		s.portalRead("portal_turmas", s.handleTurmasAbertas),
		s.permGuard("agenda", "read", true, s.handleHorariosLivres),
	} {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest("GET", "/x", nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("sem token deveria ser 401, got %d", w.Code)
		}
	}
}
