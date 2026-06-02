package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// O auth guard rejeita sem tocar o banco nos caminhos de "sem token" e
// "token inválido" — então dá pra testar a fronteira de segurança sem Postgres.
func newTestServer() *Server {
	return &Server{cfg: Config{JWTSecret: "segredo-de-teste"}}
}

func TestAuthGuardRejectsMissingToken(t *testing.T) {
	s := newTestServer()
	called := false
	h := s.authGuard(func(http.ResponseWriter, *http.Request) { called = true })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/claude/conversations", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("esperava 401, veio %d", rec.Code)
	}
	if called {
		t.Fatal("handler protegido não deveria ter sido chamado")
	}
	if !strings.Contains(rec.Body.String(), "UNAUTHORIZED") {
		t.Fatalf("esperava código UNAUTHORIZED, veio %s", rec.Body.String())
	}
}

func TestAuthGuardRejectsInvalidToken(t *testing.T) {
	s := newTestServer()
	h := s.authGuard(func(http.ResponseWriter, *http.Request) {})

	req := httptest.NewRequest("GET", "/claude/conversations", nil)
	req.Header.Set("Authorization", "Bearer token-invalido")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("esperava 401, veio %d", rec.Code)
	}
}

func TestBearerTokenPrefersCookie(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "access_token", Value: "do-cookie"})
	req.Header.Set("Authorization", "Bearer do-header")
	if got := bearerToken(req); got != "do-cookie" {
		t.Fatalf("esperava token do cookie, veio %q", got)
	}
}

func TestBearerTokenFallsBackToHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer do-header")
	if got := bearerToken(req); got != "do-header" {
		t.Fatalf("esperava token do header, veio %q", got)
	}
}
