package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	task := &Task{ResponsavelID: &resp, CreatedBy: &creator}

	if !canSeeTask(task, resp, false) {
		t.Fatal("responsável deveria ver a própria tarefa")
	}
	if !canSeeTask(task, creator, false) {
		t.Fatal("criador deveria ver a própria tarefa")
	}
	if canSeeTask(task, other, false) {
		t.Fatal("staff comum não deveria ver tarefa alheia")
	}
	if !canSeeTask(task, other, true) {
		t.Fatal("admin deveria ver qualquer tarefa")
	}
}
