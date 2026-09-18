package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Sem s.portalDB (harness sem Postgres real — ver nota em
// handlers_social_test.go), só o ramo de validação que retorna ANTES de
// tocar o banco é testável aqui: "dates" sem "studentId" tem que recusar
// antes de qualquer query, senão criaria aula sem saber de quem é a presença.
func TestHandlePortalGenerateSessionsImportSemStudentID(t *testing.T) {
	s := testServer(Config{})
	body := strings.NewReader(`{"dates":["2022-03-12","2022-03-19"]}`)
	r := httptest.NewRequest(http.MethodPost, "/portal/classes/1/sessions", body)
	r.SetPathValue("classId", "1")
	w := httptest.NewRecorder()
	s.handlePortalGenerateSessions(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperado 400 (studentId ausente)", w.Code)
	}
}
