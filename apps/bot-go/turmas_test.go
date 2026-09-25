package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// agoraTurmas: 25/09/2026 15h em Ribeirão Preto.
var agoraTurmas = time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC)

func turmaAoVivoDeTeste() TurmaAoVivo {
	t := TurmaAoVivo{ID: 22, Nome: "Turma Programação", Inicio: "2026-08-01", FimPrevisto: "2027-08-01",
		Alunos: 3, Capacidade: 10, Vagas: 7,
		Horarios: []HorarioTurma{{DiaSemana: 6, HoraInicio: "13:00", HoraFim: "15:00"}}}
	t.Curso.Nome = "Programação"
	return t
}

func retratoDeTeste(capturado time.Time) TurmaRetrato {
	return TurmaRetrato{TurmaID: 22, Nome: "Turma Programação", Curso: "Programação",
		Horarios: []HorarioTurma{{DiaSemana: 6, HoraInicio: "13:00", HoraFim: "15:00"}},
		Inicio:   "2026-08-01", FimPrevisto: "2027-08-01", Vagas: 7, CapturadoEm: capturado}
}

// ── Tabela de decisão (spec, "Memória do bot") — um teste por linha ─────────

func TestDecideTurmasAoVivo(t *testing.T) {
	e := decideTurmas([]TurmaAoVivo{turmaAoVivoDeTeste()}, nil, nil, agoraTurmas)
	if e.Origem != TurmasAoVivo || len(e.Turmas) != 1 || !e.Turmas[0].VagasConfirmadas || e.Turmas[0].Vagas != 7 {
		t.Fatalf("ao vivo: tudo afirmado, got %+v", e)
	}
}

func TestDecideTurmasFalhouRetratoRecente(t *testing.T) {
	r := retratoDeTeste(agoraTurmas.Add(-48 * time.Hour))
	e := decideTurmas(nil, errors.New("timeout"), []TurmaRetrato{r}, agoraTurmas)
	if e.Origem != TurmasDoRetrato || len(e.Turmas) != 1 || !e.Turmas[0].VagasConfirmadas {
		t.Fatalf("retrato ≤ 3 dias: igual ao vivo, got %+v", e)
	}
}

func TestDecideTurmasFalhouRetratoVelho(t *testing.T) {
	r := retratoDeTeste(agoraTurmas.Add(-4 * 24 * time.Hour))
	e := decideTurmas(nil, errors.New("503"), []TurmaRetrato{r}, agoraTurmas)
	if e.Origem != TurmasDoRetrato || len(e.Turmas) != 1 || e.Turmas[0].VagasConfirmadas {
		t.Fatalf("retrato > 3 dias: turma sim, vaga com ressalva, got %+v", e)
	}
}

func TestDecideTurmasRetratoSumidoOuVencidoNuncaAfirma(t *testing.T) {
	sumiu := agoraTurmas.Add(-time.Hour)
	r1 := retratoDeTeste(agoraTurmas.Add(-time.Hour))
	r1.SumiuEm = &sumiu
	r2 := retratoDeTeste(agoraTurmas.Add(-time.Hour))
	r2.TurmaID, r2.FimPrevisto = 23, "2026-09-24" // venceu ontem
	e := decideTurmas(nil, errors.New("x"), []TurmaRetrato{r1, r2}, agoraTurmas)
	if len(e.Turmas) != 0 || e.Origem != TurmasSemDados {
		t.Fatalf("sumida/vencida nunca é afirmada, got %+v", e)
	}
}

func TestDecideTurmasFimHojeAindaVale(t *testing.T) {
	r := retratoDeTeste(agoraTurmas.Add(-time.Hour))
	r.FimPrevisto = "2026-09-25"
	if e := decideTurmas(nil, errors.New("x"), []TurmaRetrato{r}, agoraTurmas); len(e.Turmas) != 1 {
		t.Fatalf("fim hoje ainda vale, got %+v", e)
	}
}

func TestDecideTurmasFalhouSemRetrato(t *testing.T) {
	e := decideTurmas(nil, errors.New("x"), nil, agoraTurmas)
	if e.Origem != TurmasSemDados || len(e.Turmas) != 0 {
		t.Fatalf("sem nada: comportamento de hoje, got %+v", e)
	}
}

// Lista ao vivo vazia = nenhuma turma LIBERADA, não "a escola não tem turma".
// Tratar como afirmável deixaria o bot dizer que não há turma (fail-open).
func TestDecideTurmasAoVivoVazioNaoAfirmaAusencia(t *testing.T) {
	e := decideTurmas([]TurmaAoVivo{}, nil, nil, agoraTurmas)
	if e.Origem != TurmasSemDados {
		t.Fatalf("lista vazia não pode virar 'não temos turma', got %+v", e)
	}
}

func TestDecideTurmasAoVivoDescartaVencida(t *testing.T) {
	v := turmaAoVivoDeTeste()
	v.FimPrevisto = "2026-09-20"
	if e := decideTurmas([]TurmaAoVivo{v}, nil, nil, agoraTurmas); len(e.Turmas) != 0 {
		t.Fatalf("defesa: vencida vinda da API também não é afirmada, got %+v", e)
	}
}

// ── Bloco do prompt ─────────────────────────────────────────────────────────

func estadoAoVivo() EstadoTurmas {
	e := decideTurmas([]TurmaAoVivo{turmaAoVivoDeTeste()}, nil, nil, agoraTurmas)
	e.Janelas = []JanelaTurma{{DiaSemana: 6, HoraInicio: "08:00", HoraFim: "13:00"}, {DiaSemana: 1, HoraInicio: "08:00", HoraFim: "17:00"}}
	return e
}

func TestBlocoDasTurmasSoParaIdadeDeTurma(t *testing.T) {
	e := estadoAoVivo()
	for _, idade := range []int{0, 17, 45} {
		if b := (Qualificacao{AlunoIdade: idade}).BlocoDasTurmas(RegrasVenda{}, e, agoraTurmas); b != "" {
			t.Fatalf("idade %d não deveria receber o bloco:\n%s", idade, b)
		}
	}
	for _, idade := range []int{8, 12, 15, 16} {
		if b := (Qualificacao{AlunoIdade: idade}).BlocoDasTurmas(RegrasVenda{}, e, agoraTurmas); !strings.Contains(b, "# Turmas abertas agora") {
			t.Fatalf("idade %d deveria receber o bloco", idade)
		}
	}
}

func TestBlocoDasTurmasAoVivo(t *testing.T) {
	b := Qualificacao{AlunoIdade: 12}.BlocoDasTurmas(RegrasVenda{}, estadoAoVivo(), agoraTurmas)
	for _, deve := range []string{
		"Turma Programação", "Programação", "Sábado 13:00–15:00", "01/08/2026", "1 mês de aula", "08/2027",
		"Situação: tem vaga", "Segunda 08:00–17:00", "Sábado 08:00–13:00", "Oportunidade de turma nova",
		"não há turma confirmada nesse horário",
	} {
		if !strings.Contains(b, deve) {
			t.Fatalf("bloco sem %q:\n%s", deve, b)
		}
	}
	// Não cita número de alunos nem de vagas (trava existente: não citar número de alunos de turma).
	for _, nao := range []string{"7 vagas", "3 alunos", "10 alunos"} {
		if strings.Contains(b, nao) {
			t.Fatalf("bloco não pode citar %q:\n%s", nao, b)
		}
	}
	// A ordem dos dias das janelas segue a semana (segunda antes de sábado).
	if strings.Index(b, "Segunda 08:00–17:00") > strings.Index(b, "Sábado 08:00–13:00") {
		t.Fatalf("janelas fora de ordem:\n%s", b)
	}
}

func TestBlocoDasTurmasCheiaNaoOferece(t *testing.T) {
	v := turmaAoVivoDeTeste()
	v.Vagas = 0
	e := decideTurmas([]TurmaAoVivo{v}, nil, nil, agoraTurmas)
	b := Qualificacao{AlunoIdade: 12}.BlocoDasTurmas(RegrasVenda{}, e, agoraTurmas)
	if !strings.Contains(b, "turma cheia") || strings.Contains(b, "Situação: tem vaga") {
		t.Fatalf("turma cheia:\n%s", b)
	}
}

func TestBlocoDasTurmasRetratoVelhoComRessalva(t *testing.T) {
	r := retratoDeTeste(agoraTurmas.Add(-5 * 24 * time.Hour))
	e := decideTurmas(nil, errors.New("x"), []TurmaRetrato{r}, agoraTurmas)
	b := Qualificacao{AlunoIdade: 12}.BlocoDasTurmas(RegrasVenda{}, e, agoraTurmas)
	if !strings.Contains(b, "tinha vaga em 20/09") || !strings.Contains(b, "equipe confirma") || strings.Contains(b, "Horários livres") {
		t.Fatalf("retrato velho: vaga com ressalva e sem janelas:\n%s", b)
	}
}

func TestBlocoDasTurmasSemDadosSemJanelasVazio(t *testing.T) {
	if b := (Qualificacao{AlunoIdade: 12}).BlocoDasTurmas(RegrasVenda{}, EstadoTurmas{}, agoraTurmas); b != "" {
		t.Fatalf("sem dados nenhum: sem bloco (comportamento de hoje), got:\n%s", b)
	}
}

func TestBlocoDasTurmasSoJanelas(t *testing.T) {
	e := EstadoTurmas{Janelas: []JanelaTurma{{DiaSemana: 2, HoraInicio: "14:00", HoraFim: "16:00"}}}
	b := Qualificacao{AlunoIdade: 12}.BlocoDasTurmas(RegrasVenda{}, e, agoraTurmas)
	if !strings.Contains(b, "Terça 14:00–16:00") || strings.Contains(b, "não há turma confirmada") {
		t.Fatalf("só janelas: sem afirmar ausência de turma:\n%s", b)
	}
}

// ── Fonte: cache, gravação do retrato e reserva ─────────────────────────────

type apiTurmasFake struct {
	mu        sync.Mutex
	turmas    []TurmaAoVivo
	err       error
	errJanela error
	chamadas  int
	params    string
}

func (a *apiTurmasFake) TurmasAbertas(ctx context.Context) ([]TurmaAoVivo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.chamadas++
	return a.turmas, a.err
}

func (a *apiTurmasFake) HorariosLivres(ctx context.Context, abre, fecha string, duracao int, dias []int) ([]JanelaTurma, error) {
	a.params = abre + "|" + fecha
	if a.errJanela != nil {
		return nil, a.errJanela
	}
	return []JanelaTurma{{DiaSemana: 1, HoraInicio: abre, HoraFim: fecha}}, nil
}

type retratoFake struct {
	gravadas []TurmaAoVivo
	lista    []TurmaRetrato
	err      error
}

func (r *retratoFake) Grava(ctx context.Context, tenant TenantID, ts []TurmaAoVivo, agora time.Time) error {
	r.gravadas = ts
	return r.err
}

func (r *retratoFake) Lista(ctx context.Context, tenant TenantID) ([]TurmaRetrato, error) {
	return r.lista, r.err
}

func TestTurmasFonteGravaRetratoECacheia(t *testing.T) {
	api := &apiTurmasFake{turmas: []TurmaAoVivo{turmaAoVivoDeTeste()}}
	ret := &retratoFake{}
	f := NewTurmasFonte(api, ret, 5*time.Minute, nil)
	f.agora = func() time.Time { return agoraTurmas }
	e := f.Estado(context.Background(), "t1", "08:00", "22:00")
	if e.Origem != TurmasAoVivo || len(ret.gravadas) != 1 || len(e.Janelas) != 1 || api.params != "08:00|22:00" {
		t.Fatalf("ao vivo deveria gravar retrato e trazer janelas: %+v gravadas=%d", e, len(ret.gravadas))
	}
	f.Estado(context.Background(), "t1", "08:00", "22:00")
	if api.chamadas != 1 {
		t.Fatalf("dentro do TTL não consulta de novo, chamadas=%d", api.chamadas)
	}
	f.agora = func() time.Time { return agoraTurmas.Add(6 * time.Minute) }
	f.Estado(context.Background(), "t1", "08:00", "22:00")
	if api.chamadas != 2 {
		t.Fatalf("depois do TTL consulta de novo, chamadas=%d", api.chamadas)
	}
}

func TestTurmasFonteFalhaUsaRetrato(t *testing.T) {
	api := &apiTurmasFake{err: errors.New("503")}
	ret := &retratoFake{lista: []TurmaRetrato{retratoDeTeste(agoraTurmas.Add(-time.Hour))}}
	f := NewTurmasFonte(api, ret, 5*time.Minute, nil)
	f.agora = func() time.Time { return agoraTurmas }
	e := f.Estado(context.Background(), "t1", "08:00", "22:00")
	if e.Origem != TurmasDoRetrato || len(e.Turmas) != 1 || e.Janelas != nil || ret.gravadas != nil {
		t.Fatalf("falha: usa retrato, sem janelas, sem gravar: %+v", e)
	}
}

func TestTurmasFonteRetratoForaDoArNaoQuebra(t *testing.T) {
	api := &apiTurmasFake{err: errors.New("503")}
	ret := &retratoFake{err: errors.New("banco fora")}
	f := NewTurmasFonte(api, ret, 5*time.Minute, nil)
	if e := f.Estado(context.Background(), "t1", "08:00", "22:00"); e.Origem != TurmasSemDados {
		t.Fatalf("API e banco fora: sem dados, got %+v", e)
	}
}

func TestTurmasFonteNilSemDados(t *testing.T) {
	var f *TurmasFonte
	if e := f.Estado(context.Background(), "t1", "08:00", "22:00"); e.Origem != TurmasSemDados || e.Janelas != nil {
		t.Fatalf("sem fonte configurada: sem dados, got %+v", e)
	}
}

func TestBuildPromptIncluiTurmasSoComDados(t *testing.T) {
	ctx := ConversationContext{Qualificacao: Qualificacao{AlunoIdade: 12}}
	com := BuildPrompt(TenantConfig{Turmas: estadoAoVivo()}, ctx, "tem turma sábado?", agoraTurmas)
	if !strings.Contains(com, "# Turmas abertas agora") || !strings.Contains(com, "Sábado 13:00–15:00") {
		t.Fatal("prompt com turmas ao vivo deveria trazer o bloco")
	}
	// O bloco vem depois das regras de turma × particular e antes do agendamento.
	if strings.Index(com, "# Turma ou curso particular") > strings.Index(com, "# Turmas abertas agora") ||
		strings.Index(com, "# Turmas abertas agora") > strings.Index(com, "# Agendamento de aulas") {
		t.Fatal("ordem dos blocos errada")
	}
	sem := BuildPrompt(TenantConfig{}, ctx, "tem turma sábado?", agoraTurmas)
	if strings.Contains(sem, "# Turmas abertas agora") {
		t.Fatal("sem dados, o prompt fica como antes")
	}
}
