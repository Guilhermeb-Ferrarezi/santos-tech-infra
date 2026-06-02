package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Cobre os caminhos de validação que retornam ANTES de tocar no banco — sem Postgres.
func TestHandlePreferencesValidation(t *testing.T) {
	s := testServer(Config{})

	// não-objeto (array) → 400
	w := httptest.NewRecorder()
	s.handlePreferencesUpdate(w, httptest.NewRequest("PATCH", "/auth/me/preferences", strings.NewReader(`[1,2,3]`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("array deveria ser 400, veio %d", w.Code)
	}

	// null → 400
	w2 := httptest.NewRecorder()
	s.handlePreferencesUpdate(w2, httptest.NewRequest("PATCH", "/auth/me/preferences", strings.NewReader(`null`)))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("null deveria ser 400, veio %d", w2.Code)
	}

	// escalar → 400
	w3 := httptest.NewRecorder()
	s.handlePreferencesUpdate(w3, httptest.NewRequest("PATCH", "/auth/me/preferences", strings.NewReader(`123`)))
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("escalar deveria ser 400, veio %d", w3.Code)
	}

	// corpo grande demais → 413
	big := `{"x":"` + strings.Repeat("a", 5000) + `"}`
	w4 := httptest.NewRecorder()
	s.handlePreferencesUpdate(w4, httptest.NewRequest("PATCH", "/auth/me/preferences", strings.NewReader(big)))
	if w4.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("corpo grande deveria ser 413, veio %d", w4.Code)
	}
}
