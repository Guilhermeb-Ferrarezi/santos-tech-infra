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

	// senha longa (> 128) → 400 antes do banco (evita argon2 DoS)
	w3 := httptest.NewRecorder()
	s.handleRegister(w3, httptest.NewRequest("POST", "/auth/register",
		strings.NewReader(`{"email":"a@b.com","name":"X","password":"`+strings.Repeat("a", 129)+`"}`)))
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("senha longa: code=%d", w3.Code)
	}

	// email malformado → 400 antes do banco
	for _, bad := range []string{"notanemail", "missing@tld", "@nodomain.com", "spaces in@email.com"} {
		w4 := httptest.NewRecorder()
		s.handleRegister(w4, httptest.NewRequest("POST", "/auth/register",
			strings.NewReader(`{"email":"`+bad+`","name":"X","password":"12345678"}`)))
		if w4.Code != http.StatusBadRequest {
			t.Errorf("email inválido %q: code=%d (queria 400)", bad, w4.Code)
		}
	}

	// email longo (> 254 chars, RFC 5321) → 400 antes do banco
	// 249 'a' + "@b.com" = 255 chars > 254
	w5 := httptest.NewRecorder()
	s.handleRegister(w5, httptest.NewRequest("POST", "/auth/register",
		strings.NewReader(`{"email":"`+strings.Repeat("a", 249)+"@b.com"+`","name":"X","password":"12345678"}`)))
	if w5.Code != http.StatusBadRequest {
		t.Fatalf("email longo: code=%d (queria 400)", w5.Code)
	}

	// nome só com espaços → 400 após trim (bug: sem TrimSpace passaria como não-vazio)
	w6 := httptest.NewRecorder()
	s.handleRegister(w6, httptest.NewRequest("POST", "/auth/register",
		strings.NewReader(`{"email":"a@b.com","name":"   ","password":"12345678"}`)))
	if w6.Code != http.StatusBadRequest {
		t.Fatalf("nome só espaços: code=%d (queria 400)", w6.Code)
	}

	// nome longo (> 128 chars) → 400 antes do banco
	w7 := httptest.NewRecorder()
	s.handleRegister(w7, httptest.NewRequest("POST", "/auth/register",
		strings.NewReader(`{"email":"a@b.com","name":"`+strings.Repeat("a", 129)+`","password":"12345678"}`)))
	if w7.Code != http.StatusBadRequest {
		t.Fatalf("nome longo: code=%d (queria 400)", w7.Code)
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

func TestHandleRefreshInvalidJWT(t *testing.T) {
	s := testServer(Config{JWTRefreshSecret: "secret"})
	for _, bad := range []string{"not-a-jwt", "a.b.c", ""} {
		if bad == "" {
			continue // sem cookie já coberto por TestHandleRefreshNoCookie
		}
		req := httptest.NewRequest("POST", "/auth/refresh", nil)
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: bad})
		w := httptest.NewRecorder()
		s.handleRefresh(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("token %q: code=%d (queria 401)", bad, w.Code)
		}
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
