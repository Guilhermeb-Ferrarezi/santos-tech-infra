package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func taskReq(method, id, body string, userID int64) *http.Request {
	r := httptest.NewRequest(method, "/tasks/"+id, strings.NewReader(body))
	r.SetPathValue("id", id)
	return reqAs(r, userID)
}

func TestPermGuardTasksNoToken(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.permGuard("tarefas", "read", true, func(http.ResponseWriter, *http.Request) {
		t.Fatal("não deveria passar sem token")
	})(w, httptest.NewRequest("GET", "/tasks", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestHandleTaskBadUUID(t *testing.T) {
	s := testServer(Config{})
	for _, h := range []http.HandlerFunc{
		s.handleGetTask, s.handleUpdateTask, s.handleDeleteTask,
		s.handleListTaskNotes, s.handleAddTaskNote,
		s.handleConfirmTask, s.handleUnconfirmTask,
	} {
		w := httptest.NewRecorder()
		h(w, taskReq("GET", "nao-e-uuid", "{}", 1))
		if w.Code != http.StatusNotFound {
			t.Fatalf("uuid inválido: code=%d", w.Code)
		}
	}
}

func TestHandleCreateTaskValidation(t *testing.T) {
	s := testServer(Config{})

	cases := []struct {
		name string
		body string
	}{
		{"corpo inválido", "xxx"},
		{"título vazio", `{"title":"","status":"a_fazer","priority":"media"}`},
		{"status inválido", `{"title":"T","status":"voando","priority":"media"}`},
		{"prioridade inválida", `{"title":"T","status":"a_fazer","priority":"urgentissima"}`},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		s.handleCreateTask(w, reqAs(
			httptest.NewRequest("POST", "/tasks", strings.NewReader(tc.body)), 1))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d", tc.name, w.Code)
		}
	}
}

func TestHandleAddTaskNoteValidation(t *testing.T) {
	s := testServer(Config{})

	w := httptest.NewRecorder()
	s.handleAddTaskNote(w, taskReq("POST", validUUID, `{"content":"   "}`, 1))
	// handleAddTaskNote valida o corpo ANTES de buscar a tarefa no banco (ver
	// comentário em handlers_tasks.go) — por isso este teste chega a 400 sem
	// nunca tocar s.db (que é nil no testServer de unidade). Se a ordem do
	// handler for invertida no futuro, este teste passa a exigir um Postgres
	// real e vai panicar aqui — é o sinal de alerta que protege essa ordem.
	if w.Code != http.StatusBadRequest {
		t.Fatalf("conteúdo vazio: code=%d", w.Code)
	}
}

func TestCanSeeTask(t *testing.T) {
	resp := int64(10)
	creator := int64(20)
	other := int64(30)
	coResp := int64(40)
	task := &Task{ResponsavelID: &resp, CreatedBy: &creator, CoResponsaveis: []TaskCoResponsavel{{UserID: coResp}}}

	if !canSeeTask(task, resp, false) {
		t.Fatal("responsável deveria ver a própria tarefa")
	}
	if !canSeeTask(task, creator, false) {
		t.Fatal("criador deveria ver a própria tarefa")
	}
	if !canSeeTask(task, coResp, false) {
		t.Fatal("co-responsável deveria ver a própria tarefa")
	}
	if canSeeTask(task, other, false) {
		t.Fatal("staff comum não deveria ver tarefa alheia")
	}
	if !canSeeTask(task, other, true) {
		t.Fatal("admin deveria ver qualquer tarefa")
	}
}

func TestCheckTaskConfirmationsComplete(t *testing.T) {
	confirmed := time.Now()

	// Tarefa comum (sem co-responsáveis) nunca trava, mesmo sem confirmação do
	// responsável.
	if err := checkTaskConfirmationsComplete(&Task{}); err != nil {
		t.Fatalf("tarefa comum não deveria travar: %v", err)
	}

	base := func() *Task {
		return &Task{CoResponsaveis: []TaskCoResponsavel{{UserID: 2}, {UserID: 3}}}
	}

	if err := checkTaskConfirmationsComplete(base()); err == nil {
		t.Fatal("nenhuma confirmação: deveria travar")
	}

	responsavelOnly := base()
	responsavelOnly.ResponsavelConfirmedAt = &confirmed
	if err := checkTaskConfirmationsComplete(responsavelOnly); err == nil {
		t.Fatal("só o responsável confirmou: deveria travar (co-responsáveis faltando)")
	}

	partial := base()
	partial.ResponsavelConfirmedAt = &confirmed
	partial.CoResponsaveis[0].ConfirmedAt = &confirmed
	if err := checkTaskConfirmationsComplete(partial); err == nil {
		t.Fatal("um co-responsável ainda não confirmou: deveria travar")
	}

	all := base()
	all.ResponsavelConfirmedAt = &confirmed
	all.CoResponsaveis[0].ConfirmedAt = &confirmed
	all.CoResponsaveis[1].ConfirmedAt = &confirmed
	if err := checkTaskConfirmationsComplete(all); err != nil {
		t.Fatalf("todos confirmaram: não deveria travar: %v", err)
	}
}

func TestTargetConfirmUserID(t *testing.T) {
	// Sem corpo (comum em DELETE) ou sem userId: alvo é o próprio requisitante.
	r := taskReq("DELETE", validUUID, "", 7)
	uid, err := targetConfirmUserID(r, false)
	if err != nil || uid != 7 {
		t.Fatalf("esperava (7, nil), veio (%d, %v)", uid, err)
	}

	// userId igual ao próprio: sempre aceito, mesmo sem ser admin.
	r = taskReq("POST", validUUID, `{"userId":7}`, 7)
	uid, err = targetConfirmUserID(r, false)
	if err != nil || uid != 7 {
		t.Fatalf("esperava (7, nil), veio (%d, %v)", uid, err)
	}

	// userId de outra pessoa, requisitante não-admin: 403.
	r = taskReq("POST", validUUID, `{"userId":9}`, 7)
	if _, err := targetConfirmUserID(r, false); err == nil {
		t.Fatal("não-admin confirmando por outra pessoa deveria falhar")
	}

	// userId de outra pessoa, requisitante admin: aceito (correção manual).
	r = taskReq("POST", validUUID, `{"userId":9}`, 7)
	uid, err = targetConfirmUserID(r, true)
	if err != nil || uid != 9 {
		t.Fatalf("esperava (9, nil), veio (%d, %v)", uid, err)
	}
}
