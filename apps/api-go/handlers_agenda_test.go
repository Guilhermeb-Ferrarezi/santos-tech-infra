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
	in := AgendaEventoInput{
		Tipo: "aula_turma", Titulo: "Turma", DataInicio: "2026-09-01",
		HoraInicio: "19:30", HoraFim: "21:30", ComputadoresUsados: 8,
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
		HoraInicio: "20:00", HoraFim: "22:00", ComputadoresUsados: 5,
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

func TestValidateAgendaEventoInputRecorrenciaMaxSpan(t *testing.T) {
	dia := 2
	fim := "9999-12-31"
	in := AgendaEventoInput{
		Tipo: "aula_turma", Titulo: "Turma", DataInicio: "2026-09-01",
		HoraInicio: "19:30", HoraFim: "21:30", ComputadoresUsados: 8,
		DiaSemana: &dia, DataFimRecorrencia: &fim,
	}
	if err := validateAgendaEventoInput(&in); err == nil {
		t.Fatal("dataFimRecorrencia a mais de 2 anos do início deveria ser rejeitada")
	}
}
