package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOAuthAuthorizeMissingParams(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	w := httptest.NewRecorder()
	s.handleOAuthAuthorize(w, httptest.NewRequest("GET", "/oauth/authorize", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("sem client_id/redirect_uri deveria dar 400, veio %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "VALIDATION_ERROR") {
		t.Fatalf("esperava VALIDATION_ERROR, veio %s", w.Body.String())
	}
}

func TestOAuthTokenUnsupportedGrant(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	r := httptest.NewRequest("POST", "/oauth/token", strings.NewReader("grant_type=password"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleOAuthToken(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("grant_type não suportado deveria dar 400, veio %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "UNSUPPORTED_GRANT_TYPE") {
		t.Fatalf("esperava UNSUPPORTED_GRANT_TYPE, veio %s", w.Body.String())
	}
}

func TestOAuthConfirmMissingBody(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	r := httptest.NewRequest("POST", "/oauth/authorize/confirm", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	s.handleOAuthConfirm(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("body vazio deveria dar 400, veio %d", w.Code)
	}
}
