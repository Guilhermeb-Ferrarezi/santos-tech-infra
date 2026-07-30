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

// TestOAuthConfirmInvalidRequestID garante que requestId de formato inválido é
// rejeitado por isValidOAuthRequestID antes de qualquer chamada ao Redis —
// espelha o padrão de isValidChallenge (MFA) e isValidResetToken (reset de senha).
func TestOAuthConfirmInvalidRequestID(t *testing.T) {
	s := testServer(Config{JWTSecret: "secret"})
	for _, bad := range []string{
		strings.Repeat("a", 31), // 1 char a menos (31 hex)
		strings.Repeat("a", 33), // 1 char a mais (33 hex)
		strings.Repeat("x", 32), // comprimento certo mas 'x' não é hex válido
		"",                      // vazio já pego pelo check anterior, mas garante cobertura
	} {
		if bad == "" {
			continue // vazio é rejeitado antes por "requestId e sessionId são obrigatórios"
		}
		body := `{"requestId":"` + bad + `","sessionId":"00000000-0000-0000-0000-000000000000"}`
		w := httptest.NewRecorder()
		s.handleOAuthConfirm(w, httptest.NewRequest("POST", "/oauth/authorize/confirm", strings.NewReader(body)))
		if w.Code != http.StatusGone {
			t.Errorf("requestId inválido %q: code=%d (queria 410 REQUEST_EXPIRED)", bad, w.Code)
		}
		if !strings.Contains(w.Body.String(), "REQUEST_EXPIRED") {
			t.Errorf("requestId inválido %q: body=%s (queria REQUEST_EXPIRED)", bad, w.Body.String())
		}
	}
}

// TestOAuthConfirmValidRequestIDMissingInRedis garante que um requestId de
// formato correto mas ausente no Redis devolve 410 REQUEST_EXPIRED — e não 500.
func TestOAuthConfirmValidRequestIDMissingInRedis(t *testing.T) {
	s := testServerWithRedis(t, Config{JWTSecret: "secret"})
	validID := strings.Repeat("a", 32) // 32 chars hex válidos
	body := `{"requestId":"` + validID + `","sessionId":"00000000-0000-0000-0000-000000000000"}`
	w := httptest.NewRecorder()
	s.handleOAuthConfirm(w, httptest.NewRequest("POST", "/oauth/authorize/confirm", strings.NewReader(body)))
	if w.Code != http.StatusGone {
		t.Fatalf("requestId ausente no Redis: code=%d (queria 410)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "REQUEST_EXPIRED") {
		t.Errorf("esperava REQUEST_EXPIRED no body, veio: %s", w.Body.String())
	}
}

func TestGoogleStartStoresReturnTo(t *testing.T) {
	s := testServerWithRedis(t, Config{GoogleClientID: "x"})
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
	s := testServerWithRedis(t, Config{GoogleClientID: "x"})
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
