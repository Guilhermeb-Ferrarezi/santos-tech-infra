package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleCreateGlossaryTermValidation(t *testing.T) {
	s := testServer(Config{})

	cases := []struct {
		name string
		body string
	}{
		{"corpo inválido", "xxx"},
		{"termo vazio", `{"term":"","definicao":"algo"}`},
		{"definição vazia", `{"term":"Build","definicao":"   "}`},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		s.handleCreateGlossaryTerm(w, reqAs(
			httptest.NewRequest("POST", "/glossary", strings.NewReader(tc.body)), 1))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d", tc.name, w.Code)
		}
	}
}

func TestHandleUpdateGlossaryTermBadUUID(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/glossary/nao-e-uuid", strings.NewReader(`{"term":"X","definicao":"Y"}`))
	r.SetPathValue("id", "nao-e-uuid")
	s.handleUpdateGlossaryTerm(w, reqAs(r, 1))
	if w.Code != http.StatusNotFound {
		t.Fatalf("uuid inválido: code=%d", w.Code)
	}
}

func TestHandleDeleteGlossaryTermBadUUID(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/glossary/nao-e-uuid", nil)
	r.SetPathValue("id", "nao-e-uuid")
	s.handleDeleteGlossaryTerm(w, reqAs(r, 1))
	if w.Code != http.StatusNotFound {
		t.Fatalf("uuid inválido: code=%d", w.Code)
	}
}
