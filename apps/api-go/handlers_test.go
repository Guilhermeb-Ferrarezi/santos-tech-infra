package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Estes testes cobrem só os caminhos de validação que retornam ANTES de tocar
// no banco/redis (corpo inválido, campos faltando, sem cookie) — sem precisar
// de Postgres/Redis no CI.

func TestHandleRegisterValidation(t *testing.T) {
	s := testServer(Config{})

	w := httptest.NewRecorder()
	s.handleRegister(w, httptest.NewRequest("POST", "/auth/register", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido: code=%d", w.Code)
	}

	// senha curta (< 8) → 400 antes do banco
	w2 := httptest.NewRecorder()
	s.handleRegister(w2, httptest.NewRequest("POST", "/auth/register",
		strings.NewReader(`{"email":"a@b.com","name":"X","password":"123"}`)))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("senha curta: code=%d", w2.Code)
	}
}

func TestHandleLoginBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleLogin(w, httptest.NewRequest("POST", "/auth/login", strings.NewReader("xxx")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestHandleRefreshNoCookie(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleRefresh(w, httptest.NewRequest("POST", "/auth/refresh", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestHandleMeNoToken(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleMe(w, httptest.NewRequest("GET", "/auth/me", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", w.Code)
	}
}
