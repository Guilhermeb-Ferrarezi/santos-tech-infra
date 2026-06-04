package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleAccountsListNoCookie(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	w := httptest.NewRecorder()
	s.handleAccountsList(w, httptest.NewRequest("GET", "/auth/accounts", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, `"accounts":[]`) {
		t.Fatalf("esperava lista vazia, veio %s", body)
	}
}

func TestHandleAccountDeleteNotInCookie(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	r := httptest.NewRequest("DELETE", "/auth/accounts/"+sidA, nil)
	r.SetPathValue("sessionId", sidA)
	w := httptest.NewRecorder()
	s.handleAccountDelete(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("sid fora do cookie deveria dar 404, veio %d", w.Code)
	}
}

func TestHandleAccountActivateNotInCookie(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	r := httptest.NewRequest("POST", "/auth/accounts/"+sidA+"/activate", nil)
	r.SetPathValue("sessionId", sidA)
	w := httptest.NewRecorder()
	s.handleAccountActivate(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("sid fora do cookie deveria dar 401, veio %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "SESSION_EXPIRED") {
		t.Fatalf("esperava SESSION_EXPIRED, veio %s", w.Body.String())
	}
}
