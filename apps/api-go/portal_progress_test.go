package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPortalProgressRoutesAuthBeforeDB(t *testing.T) {
	s := testServer(Config{})
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		m    string
		path string
	}{
		{"exercise answers", s.staffGuard(s.handlePortalExerciseAnswers), "GET", "/portal/exercises/1/answers"},
		{"answer students", s.staffGuard(s.handlePortalAnswerStudents), "GET", "/portal/answers/students"},
		{"update answer", s.staffGuard(s.handlePortalUpdateAnswer), "PATCH", "/portal/answers/1"},
		{"batch answers", s.staffGuard(s.handlePortalBatchUpdateAnswers), "PATCH", "/portal/answers/batch"},
		{"phase progress", s.staffGuard(s.handlePortalPhaseProgress), "GET", "/portal/phases/1/progress"},
		{"class progress", s.staffGuard(s.handlePortalClassProgress), "GET", "/portal/classes/1/progress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.m, tc.path, nil)
			w := httptest.NewRecorder()
			tc.h(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("code=%d want 401", w.Code)
			}
		})
	}
}

func TestPortalAnswerValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})

	// patch vazio → 400
	r := httptest.NewRequest("PATCH", "/portal/answers/1", strings.NewReader(`{}`))
	r.SetPathValue("answerId", "1")
	w := httptest.NewRecorder()
	s.handlePortalUpdateAnswer(w, reqAs(r, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("patch vazio code=%d", w.Code)
	}

	// id inválido → 400
	r2 := httptest.NewRequest("PATCH", "/portal/answers/x", strings.NewReader(`{"isCorrect":true}`))
	r2.SetPathValue("answerId", "x")
	w2 := httptest.NewRecorder()
	s.handlePortalUpdateAnswer(w2, reqAs(r2, 1))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("id inválido code=%d", w2.Code)
	}

	// batch sem answerIds → 400
	r3 := httptest.NewRequest("PATCH", "/portal/answers/batch", strings.NewReader(`{"answerIds":[],"patch":{"isCorrect":true}}`))
	w3 := httptest.NewRecorder()
	s.handlePortalBatchUpdateAnswers(w3, reqAs(r3, 1))
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("batch vazio code=%d", w3.Code)
	}

	// batch com patch vazio → 400
	r4 := httptest.NewRequest("PATCH", "/portal/answers/batch", strings.NewReader(`{"answerIds":[1,2],"patch":{}}`))
	w4 := httptest.NewRecorder()
	s.handlePortalBatchUpdateAnswers(w4, reqAs(r4, 1))
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("batch patch vazio code=%d", w4.Code)
	}
}

func TestPortalProgressStatus(t *testing.T) {
	two := 2
	if st, lbl := portalProgressStatus(&two, nil); st != 2 || lbl != "concluido" {
		t.Fatalf("status 2: %d %q", st, lbl)
	}
	zero := 0
	if st, lbl := portalProgressStatus(&zero, nil); st != 0 || lbl != "nao_iniciado" {
		t.Fatalf("status 0: %d %q", st, lbl)
	}
	// status NULL + unlocked_at → em progresso
	now := portalNowUTC()
	if st, lbl := portalProgressStatus(nil, &now); st != 1 || lbl != "em_progresso" {
		t.Fatalf("null+unlocked: %d %q", st, lbl)
	}
}
