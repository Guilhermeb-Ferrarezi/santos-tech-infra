package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleCreateAPIKeyValidation cobre os caminhos de validação que retornam
// antes de tocar no banco — sem precisar de Postgres/Redis no CI.
func TestHandleCreateAPIKeyValidation(t *testing.T) {
	s := testServer(Config{})

	// corpo inválido → 400
	w := httptest.NewRecorder()
	s.handleCreateAPIKey(w, httptest.NewRequest("POST", "/auth/api-keys", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido: code=%d (queria 400)", w.Code)
	}

	// nome vazio → 400
	w2 := httptest.NewRecorder()
	s.handleCreateAPIKey(w2, httptest.NewRequest("POST", "/auth/api-keys",
		strings.NewReader(`{"name":"  ","expiresInDays":0}`)))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("nome vazio: code=%d (queria 400)", w2.Code)
	}

	// nome longo (> 100) → 400
	w3 := httptest.NewRecorder()
	s.handleCreateAPIKey(w3, httptest.NewRequest("POST", "/auth/api-keys",
		strings.NewReader(`{"name":"`+strings.Repeat("a", 101)+`","expiresInDays":0}`)))
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("nome longo: code=%d (queria 400)", w3.Code)
	}

	// expiresInDays negativo → 400
	w4 := httptest.NewRecorder()
	s.handleCreateAPIKey(w4, httptest.NewRequest("POST", "/auth/api-keys",
		strings.NewReader(`{"name":"my-key","expiresInDays":-1}`)))
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("expiresInDays negativo: code=%d (queria 400)", w4.Code)
	}

	// expiresInDays > 3650 → 400 (evita overflow de time.Duration)
	w5 := httptest.NewRecorder()
	s.handleCreateAPIKey(w5, httptest.NewRequest("POST", "/auth/api-keys",
		strings.NewReader(`{"name":"my-key","expiresInDays":3651}`)))
	if w5.Code != http.StatusBadRequest {
		t.Fatalf("expiresInDays > 3650: code=%d (queria 400)", w5.Code)
	}

	// expiresInDays muito grande (causaria overflow se não validado) → 400
	w6 := httptest.NewRecorder()
	s.handleCreateAPIKey(w6, httptest.NewRequest("POST", "/auth/api-keys",
		strings.NewReader(`{"name":"my-key","expiresInDays":999999999}`)))
	if w6.Code != http.StatusBadRequest {
		t.Fatalf("expiresInDays overflow: code=%d (queria 400)", w6.Code)
	}
}

// TestHandleResetPasswordValidation cobre as validações de corpo/token/senha
// que retornam antes de tocar no Redis ou banco.
func TestHandleResetPasswordValidation(t *testing.T) {
	s := testServer(Config{})

	// corpo inválido → 400
	w := httptest.NewRecorder()
	s.handleResetPassword(w, httptest.NewRequest("POST", "/auth/reset-password", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido: code=%d (queria 400)", w.Code)
	}

	// senha curta (< 8) → 400
	w2 := httptest.NewRecorder()
	s.handleResetPassword(w2, httptest.NewRequest("POST", "/auth/reset-password",
		strings.NewReader(`{"token":"`+strings.Repeat("a", 64)+`","newPassword":"curta"}`)))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("senha curta: code=%d (queria 400)", w2.Code)
	}

	// senha longa (> 128) → 400 antes do Redis (evita argon2 DoS com string enorme)
	w3 := httptest.NewRecorder()
	s.handleResetPassword(w3, httptest.NewRequest("POST", "/auth/reset-password",
		strings.NewReader(`{"token":"`+strings.Repeat("a", 64)+`","newPassword":"`+strings.Repeat("x", 129)+`"}`)))
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("senha longa: code=%d (queria 400)", w3.Code)
	}

	// token com formato inválido (não é 64 hex lowercase chars) → 400 antes do Redis
	for _, badToken := range []string{
		"",
		"curto",
		strings.Repeat("a", 63), // 1 char a menos
		strings.Repeat("a", 65), // 1 char a mais
		strings.Repeat("g", 64), // comprimento certo mas 'g' não é hex
	} {
		w4 := httptest.NewRecorder()
		s.handleResetPassword(w4, httptest.NewRequest("POST", "/auth/reset-password",
			strings.NewReader(`{"token":"`+badToken+`","newPassword":"senha-valida-ok"}`)))
		if w4.Code != http.StatusBadRequest {
			t.Errorf("token inválido %q: code=%d (queria 400)", badToken, w4.Code)
		}
	}
}
