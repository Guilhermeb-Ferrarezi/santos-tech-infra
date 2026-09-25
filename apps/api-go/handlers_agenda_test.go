package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func agendaReq(method, id, body string, userID int64) *http.Request {
	r := httptest.NewRequest(method, "/agenda/eventos/"+id, strings.NewReader(body))
	r.SetPathValue("id", id)
	return reqAs(r, userID)
}

func TestPermGuardAgendaNoToken(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.permGuard("agenda", "read", true, func(http.ResponseWriter, *http.Request) {
		t.Fatal("não deveria passar sem token")
	})(w, httptest.NewRequest("GET", "/agenda/eventos", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestHandleAgendaEventoBadUUID(t *testing.T) {
	s := testServer(Config{})
	for _, h := range []http.HandlerFunc{s.handleGetAgendaEvento, s.handleUpdateAgendaEvento, s.handleDeleteAgendaEvento} {
		w := httptest.NewRecorder()
		h(w, agendaReq("GET", "nao-e-uuid", "{}", 1))
		if w.Code != http.StatusNotFound {
			t.Fatalf("uuid inválido: code=%d", w.Code)
		}
	}
}

func TestHandleCreateAgendaEventoValidation(t *testing.T) {
	s := testServer(Config{})
	cases := []struct {
		name string
		body string
	}{
		{"corpo inválido", "xxx"},
		{"título vazio", `{"titulo":"","tipo":"avulso","dataInicio":"2026-09-04","horaInicio":"20:00","horaFim":"22:00","computadoresUsados":5}`},
		{"tipo inválido", `{"titulo":"T","tipo":"invalido","dataInicio":"2026-09-04","horaInicio":"20:00","horaFim":"22:00","computadoresUsados":5}`},
		{"hora fim antes de hora início", `{"titulo":"T","tipo":"avulso","dataInicio":"2026-09-04","horaInicio":"22:00","horaFim":"20:00","computadoresUsados":5}`},
		{"pcs negativo", `{"titulo":"T","tipo":"avulso","dataInicio":"2026-09-04","horaInicio":"20:00","horaFim":"22:00","computadoresUsados":-1}`},
		{"pcs acima do teto", `{"titulo":"T","tipo":"avulso","dataInicio":"2026-09-04","horaInicio":"20:00","horaFim":"22:00","computadoresUsados":1001}`},
		{"aula_turma sem diaSemana", `{"titulo":"T","tipo":"aula_turma","dataInicio":"2026-09-04","horaInicio":"19:30","horaFim":"21:30","computadoresUsados":8}`},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		s.handleCreateAgendaEvento(w, reqAs(httptest.NewRequest("POST", "/agenda/eventos", strings.NewReader(tc.body)), 1))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d body=%s", tc.name, w.Code, w.Body.String())
		}
	}
}

func TestValidateAgendaEventoInputNormalizaRecorrencia(t *testing.T) {
	dia := 2
	fim := "2026-12-01"
	pcs8, pcs5 := 8, 5
	in := AgendaEventoInput{
		Tipo: "aula_turma", Titulo: "Turma", DataInicio: "2026-09-01",
		HoraInicio: "19:30", HoraFim: "21:30", ComputadoresUsados: &pcs8,
		DiaSemana: &dia, DataFimRecorrencia: &fim,
	}
	if err := validateAgendaEventoInput(&in); err != nil {
		t.Fatalf("não esperava erro: %v", err)
	}
	if in.Recorrencia != "semanal" {
		t.Fatalf("aula_turma deveria forçar recorrencia=semanal, got %q", in.Recorrencia)
	}

	in2 := AgendaEventoInput{
		Tipo: "avulso", Titulo: "Mix", DataInicio: "2026-09-04",
		HoraInicio: "20:00", HoraFim: "22:00", ComputadoresUsados: &pcs5,
	}
	if err := validateAgendaEventoInput(&in2); err != nil {
		t.Fatalf("não esperava erro: %v", err)
	}
	if in2.Recorrencia != "nenhuma" {
		t.Fatalf("avulso deveria forçar recorrencia=nenhuma, got %q", in2.Recorrencia)
	}
	if in2.StatusPreparo == nil || *in2.StatusPreparo != "pendente" {
		t.Fatal("avulso sem statusPreparo deveria default pra 'pendente'")
	}
}

// aula_particular pode ser semanal (aluno com horário fixo) ou avulsa — a
// escolha é do cliente. Os demais tipos continuam com a regra fixa.
func TestValidateAgendaEventoInputParticularRecorrencia(t *testing.T) {
	dia := 3
	pcs := 1
	semanal := AgendaEventoInput{
		Tipo: "aula_particular", Titulo: "Walisson", DataInicio: "2026-09-02",
		HoraInicio: "14:00", HoraFim: "15:00", ComputadoresUsados: &pcs,
		Recorrencia: "semanal", DiaSemana: &dia,
	}
	if err := validateAgendaEventoInput(&semanal); err != nil {
		t.Fatalf("particular semanal deveria ser aceita: %v", err)
	}
	if semanal.Recorrencia != "semanal" || semanal.DiaSemana == nil || *semanal.DiaSemana != 3 {
		t.Fatalf("particular semanal deveria manter recorrencia/diaSemana, got %q %v", semanal.Recorrencia, semanal.DiaSemana)
	}

	// Sem recorrencia (ou valor desconhecido) = avulsa: comportamento de antes.
	for _, rec := range []string{"", "nenhuma", "mensal"} {
		avulsa := AgendaEventoInput{
			Tipo: "aula_particular", Titulo: "Lorena", DataInicio: "2026-09-28",
			HoraInicio: "15:15", HoraFim: "16:15", ComputadoresUsados: &pcs,
			Recorrencia: rec, DiaSemana: &dia,
		}
		if err := validateAgendaEventoInput(&avulsa); err != nil {
			t.Fatalf("particular recorrencia=%q deveria ser aceita: %v", rec, err)
		}
		if avulsa.Recorrencia != "nenhuma" || avulsa.DiaSemana != nil {
			t.Fatalf("particular recorrencia=%q deveria virar avulsa, got %q %v", rec, avulsa.Recorrencia, avulsa.DiaSemana)
		}
	}

	// Semanal sem dia da semana continua 400.
	semDia := AgendaEventoInput{
		Tipo: "aula_particular", Titulo: "Igor", DataInicio: "2026-09-02",
		HoraInicio: "14:00", HoraFim: "15:00", ComputadoresUsados: &pcs,
		Recorrencia: "semanal",
	}
	if err := validateAgendaEventoInput(&semDia); err == nil {
		t.Fatal("particular semanal sem diaSemana deveria ser rejeitada")
	}

	// Experimental pedindo semanal continua forçada pra nenhuma.
	exp := AgendaEventoInput{
		Tipo: "aula_experimental", Titulo: "Exp", DataInicio: "2026-09-02",
		HoraInicio: "14:00", HoraFim: "15:00", ComputadoresUsados: &pcs,
		Recorrencia: "semanal", DiaSemana: &dia,
	}
	if err := validateAgendaEventoInput(&exp); err != nil {
		t.Fatalf("não esperava erro: %v", err)
	}
	if exp.Recorrencia != "nenhuma" {
		t.Fatalf("experimental deveria forçar recorrencia=nenhuma, got %q", exp.Recorrencia)
	}
}

func TestValidateAgendaEventoInputRecorrenciaMaxSpan(t *testing.T) {
	dia := 2
	fim := "9999-12-31"
	pcs := 8
	in := AgendaEventoInput{
		Tipo: "aula_turma", Titulo: "Turma", DataInicio: "2026-09-01",
		HoraInicio: "19:30", HoraFim: "21:30", ComputadoresUsados: &pcs,
		DiaSemana: &dia, DataFimRecorrencia: &fim,
	}
	if err := validateAgendaEventoInput(&in); err == nil {
		t.Fatal("dataFimRecorrencia a mais de 2 anos do início deveria ser rejeitada")
	}
}

// aula_turma sem dataFimRecorrencia agora é um estado válido (recorrência
// indefinida) — antes disso era 400 "obrigatória".
func TestValidateAgendaEventoInputRecorrenciaIndefinidaValida(t *testing.T) {
	dia := 2
	pcs := 8
	in := AgendaEventoInput{
		Tipo: "aula_turma", Titulo: "Turma", DataInicio: "2026-09-01",
		HoraInicio: "19:30", HoraFim: "21:30", ComputadoresUsados: &pcs,
		DiaSemana: &dia, DataFimRecorrencia: nil,
	}
	if err := validateAgendaEventoInput(&in); err != nil {
		t.Fatalf("dataFimRecorrencia nil deveria ser aceita como recorrência indefinida: %v", err)
	}
	if in.DataFimRecorrencia != nil {
		t.Fatal("dataFimRecorrencia deveria continuar nil")
	}

	// String vazia (form em branco) também normaliza pra nil, não erro.
	vazia := ""
	in2 := AgendaEventoInput{
		Tipo: "aula_turma", Titulo: "Turma", DataInicio: "2026-09-01",
		HoraInicio: "19:30", HoraFim: "21:30", ComputadoresUsados: &pcs,
		DiaSemana: &dia, DataFimRecorrencia: &vazia,
	}
	if err := validateAgendaEventoInput(&in2); err != nil {
		t.Fatalf("dataFimRecorrencia vazia deveria normalizar pra indefinida: %v", err)
	}
	if in2.DataFimRecorrencia != nil {
		t.Fatal("dataFimRecorrencia vazia deveria virar nil")
	}
}

// computadoresUsados nil ("não informado") é um estado válido — só o range
// [0,1000] é rejeitado quando um valor É enviado.
func TestValidateAgendaEventoInputComputadoresUsadosNilValido(t *testing.T) {
	in := AgendaEventoInput{
		Tipo: "avulso", Titulo: "T", DataInicio: "2026-09-04",
		HoraInicio: "20:00", HoraFim: "22:00", ComputadoresUsados: nil,
	}
	if err := validateAgendaEventoInput(&in); err != nil {
		t.Fatalf("computadoresUsados nil não deveria ser rejeitado: %v", err)
	}
}

func TestAgendaTipoCombinaComTurma(t *testing.T) {
	casos := []struct {
		tipo       string
		individual bool
		ok         bool
	}{
		{"aula_turma", false, true},
		{"aula_turma", true, false},
		{"aula_particular", true, true},
		{"aula_particular", false, false},
		{"aula_experimental", false, false},
		{"mix", false, false},
		{"dia_inteiro", true, false},
	}
	for _, c := range casos {
		if got := agendaTipoCombinaComTurma(c.tipo, c.individual); got != c.ok {
			t.Fatalf("%s individual=%v: got %v, want %v", c.tipo, c.individual, got, c.ok)
		}
	}
}
