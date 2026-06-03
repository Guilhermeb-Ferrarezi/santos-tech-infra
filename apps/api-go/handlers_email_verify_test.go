package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Cobre os caminhos de validação do confirm que retornam ANTES de tocar no Redis/banco.
func TestEmailVerifyConfirmValidation(t *testing.T) {
	s := testServer(Config{})

	// corpo inválido → 400
	w := httptest.NewRecorder()
	s.handleEmailVerifyConfirm(w, httptest.NewRequest("POST", "/auth/email-verify/confirm", strings.NewReader("xx")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido deveria ser 400, veio %d", w.Code)
	}

	// código curto → 400 (sem consultar o Redis)
	w2 := httptest.NewRecorder()
	s.handleEmailVerifyConfirm(w2, httptest.NewRequest("POST", "/auth/email-verify/confirm", strings.NewReader(`{"code":"123"}`)))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("código curto deveria ser 400, veio %d", w2.Code)
	}

	// código longo → 400
	w3 := httptest.NewRecorder()
	s.handleEmailVerifyConfirm(w3, httptest.NewRequest("POST", "/auth/email-verify/confirm", strings.NewReader(`{"code":"1234567"}`)))
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("código longo deveria ser 400, veio %d", w3.Code)
	}
}
