package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
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

func TestGoogleStartStoresReturnTo(t *testing.T) {
	s := testServer(Config{GoogleClientID: "x"})
	s.google = &oauth2.Config{ClientID: "x"}
	r := httptest.NewRequest("GET", "/auth/google?return_to=/oauth/choose%3Frequest_id%3Dabc", nil)
	w := httptest.NewRecorder()
	s.handleGoogleStart(w, r)
	var stateCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "oauth_state" {
			stateCookie = c
		}
	}
	if stateCookie == nil || !strings.Contains(stateCookie.Value, "|/oauth/choose?request_id=abc") {
		t.Fatalf("cookie oauth_state deveria carregar o return_to: %+v", stateCookie)
	}
}

func TestGoogleStartRejectsExternalReturnTo(t *testing.T) {
	s := testServer(Config{GoogleClientID: "x"})
	s.google = &oauth2.Config{ClientID: "x"}
	r := httptest.NewRequest("GET", "/auth/google?return_to=//evil.com/x", nil)
	w := httptest.NewRecorder()
	s.handleGoogleStart(w, r)
	for _, c := range w.Result().Cookies() {
		if c.Name == "oauth_state" && strings.Contains(c.Value, "evil.com") {
			t.Fatal("return_to externo não pode entrar no state")
		}
	}
}
