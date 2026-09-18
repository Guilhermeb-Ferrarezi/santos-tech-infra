package main

// Testes dos handlers de exercícios do aluno — só os caminhos que retornam
// ANTES de tocar o banco (padrão do repo, ver handlers_posaula_test.go).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPortalMyExerciseDetailInvalidID(t *testing.T) {
	s := testServer(Config{})
	r := httptest.NewRequest("GET", "/portal/me/exercises/x", nil)
	r.SetPathValue("exerciseId", "x")
	w := httptest.NewRecorder()
	s.handlePortalMyExerciseDetail(w, reqAs(r, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want %d body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestPortalMyExerciseAnswerValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})
	cases := []struct {
		name       string
		exerciseID string
		body       string
	}{
		{"exerciseId inválido", "x", `{"questionId":"1","optionId":"1"}`},
		{"corpo inválido", "1", "xxx"},
		{"questionId como número (deve ser string)", "1", `{"questionId":1,"optionId":"1"}`},
		{"sem questionId", "1", `{"optionId":"1"}`},
		{"sem optionId", "1", `{"questionId":"1"}`},
		{"ids vazios", "1", `{"questionId":"","optionId":""}`},
		{"ids não numéricos", "1", `{"questionId":"abc","optionId":"abc"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/portal/me/exercises/"+tc.exerciseID+"/answer", strings.NewReader(tc.body))
			r.SetPathValue("exerciseId", tc.exerciseID)
			w := httptest.NewRecorder()
			s.handlePortalMyExerciseAnswer(w, reqAs(r, 1))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code=%d want %d body=%s", w.Code, http.StatusBadRequest, w.Body.String())
			}
		})
	}
}
