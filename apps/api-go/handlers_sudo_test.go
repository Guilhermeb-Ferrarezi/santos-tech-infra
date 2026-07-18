package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSudoGuardSemElevacao(t *testing.T) {
	s := testServer(Config{JWTSecret: "s3cr3t"})

	// sem token nenhum → 403 SUDO_REQUIRED
	w := httptest.NewRecorder()
	s.sudoGuard(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler não deveria rodar sem sudo")
	})(w, httptest.NewRequest("DELETE", "/auth/admin/users/1", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("sem token: code=%d (queria 403)", w.Code)
	}

	// access comum (sem sudo_exp) → 403
	access, _, err := generateTokens("s3cr3t", "r3fresh", 1, "a@b.com", "")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("DELETE", "/auth/admin/users/1", nil)
	r.AddCookie(&http.Cookie{Name: "access_token", Value: access})
	w2 := httptest.NewRecorder()
	s.sudoGuard(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler não deveria rodar sem sudo_exp")
	})(w2, r)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("token sem sudo: code=%d (queria 403)", w2.Code)
	}
}

func TestSudoGuardComElevacao(t *testing.T) {
	s := testServer(Config{JWTSecret: "s3cr3t"})
	access, err := generateSudoAccess("s3cr3t", 1, "a@b.com", "", time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("DELETE", "/auth/admin/users/1", nil)
	r.AddCookie(&http.Cookie{Name: "access_token", Value: access})
	w := httptest.NewRecorder()
	ran := false
	s.sudoGuard(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
	if !ran || w.Code != http.StatusNoContent {
		t.Fatalf("ran=%v code=%d — elevação válida deveria passar", ran, w.Code)
	}
}

func TestSudoGuardElevacaoExpirada(t *testing.T) {
	s := testServer(Config{JWTSecret: "s3cr3t"})
	access, err := generateSudoAccess("s3cr3t", 1, "a@b.com", "", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("DELETE", "/auth/admin/users/1", nil)
	r.AddCookie(&http.Cookie{Name: "access_token", Value: access})
	w := httptest.NewRecorder()
	s.sudoGuard(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler não deveria rodar com sudo expirado")
	})(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("sudo expirado: code=%d (queria 403)", w.Code)
	}
}

func TestTokenSudoUntilSecretErrado(t *testing.T) {
	access, _ := generateSudoAccess("s3cr3t", 1, "a@b.com", "", time.Now().Add(10*time.Minute))
	if !tokenSudoUntil(access, "outro-secret").IsZero() {
		t.Fatal("sudo_exp não pode valer com secret errado")
	}
}

func TestHandleSudoVerifyCacheControlNoStore(t *testing.T) {
	s := testServer(Config{JWTSecret: "s3cr3t"})
	// Corpo inválido → retorna 400 antes de tocar em DB/Redis, mas o header deve
	// estar presente em TODOS os caminhos de código, incluindo erros.
	w := httptest.NewRecorder()
	s.handleSudoVerify(w, httptest.NewRequest("POST", "/auth/sudo/verify", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido: code=%d (queria 400)", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q; queria %q", got, "no-store")
	}
}
