package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQuizErrStatus(t *testing.T) {
	casos := []struct {
		err   error
		code  string
		http_ int
	}{
		{errQuizUnparseable, "UNPARSEABLE", http.StatusUnprocessableEntity},
		{errQuizTimeout, "UPSTREAM_TIMEOUT", http.StatusGatewayTimeout},
		{errQuizUpstream, "UPSTREAM_FAILED", http.StatusBadGateway},
		{errAPIRouterNoActiveKeys, "NO_ACTIVE_KEYS", http.StatusServiceUnavailable},
		{errors.New("qualquer outra"), "UPSTREAM_FAILED", http.StatusBadGateway},
	}
	for _, c := range casos {
		ae := quizErr(c.err)
		if ae.Code != c.code || ae.Status != c.http_ {
			t.Errorf("quizErr(%v) = %s/%d, queria %s/%d", c.err, ae.Code, ae.Status, c.code, c.http_)
		}
	}
}

func TestIsNativeClientAceitaExtensaoFirefox(t *testing.T) {
	// Fetch de extensão Firefox SEMPRE manda Origin: moz-extension://<uuid>.
	// Sem isto o login devolve só cookies httpOnly SameSite=Lax, que a
	// extensão não consegue ler nem reenviar: logada e sem token, pra sempre.
	r := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	r.Header.Set("Origin", "moz-extension://8f3a1c2e-0000-4000-8000-abcdefabcdef")
	if !isNativeClient(r) {
		t.Error("extensão Firefox precisa receber os tokens no corpo")
	}
}

func TestIsNativeClientRecusaOrigemWeb(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	r.Header.Set("Origin", "https://santos-tech.com")
	if isNativeClient(r) {
		t.Error("origem web continua no fluxo de cookie")
	}
}

func TestIsNativeClientSemOrigem(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	if !isNativeClient(r) {
		t.Error("cliente nativo (sem Origin) continua valendo")
	}
}
