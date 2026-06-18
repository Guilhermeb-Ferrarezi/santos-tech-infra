package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Cobrem somente os caminhos de validação que retornam ANTES de tocar no
// banco/Redis (corpo inválido, challenge mal-formado) — CI sem Postgres/Redis.

func TestHandleMFAEmailBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleMFAEmail(w, httptest.NewRequest("POST", "/auth/mfa/email", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400)", w.Code)
	}
}

// TestHandleMFAEmailInvalidChallenge: challenge de formato errado é rejeitado
// por isValidChallenge antes de qualquer chamada ao Redis.
func TestHandleMFAEmailInvalidChallenge(t *testing.T) {
	s := testServer(Config{})
	for _, bad := range []string{
		"",
		"curto",
		strings.Repeat("a", 47), // 1 char a menos
		strings.Repeat("a", 49), // 1 char a mais
		strings.Repeat("x", 48), // comprimento certo mas 'x' não é hex
	} {
		w := httptest.NewRecorder()
		s.handleMFAEmail(w, httptest.NewRequest("POST", "/auth/mfa/email",
			strings.NewReader(`{"challenge":"`+bad+`"}`)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("challenge %q: code=%d (queria 400)", bad, w.Code)
		}
	}
}

// TestHandleMFAEmailMissingChallenge: formato correto mas ausente no Redis → 400.
func TestHandleMFAEmailMissingChallenge(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	w := httptest.NewRecorder()
	s.handleMFAEmail(w, httptest.NewRequest("POST", "/auth/mfa/email",
		strings.NewReader(`{"challenge":"`+strings.Repeat("a", 48)+`"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("challenge ausente no Redis: code=%d (queria 400)", w.Code)
	}
}

func TestHandleMFAVerifyBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleMFAVerify(w, httptest.NewRequest("POST", "/auth/mfa/verify", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400)", w.Code)
	}
}

// TestHandleMFAVerifyInvalidChallenge: challenge mal-formado é rejeitado por
// isValidChallenge antes de Incr no Redis (proteção de tentativas).
func TestHandleMFAVerifyInvalidChallenge(t *testing.T) {
	s := testServer(Config{})
	for _, bad := range []string{
		"",
		"x",
		strings.Repeat("a", 47),
		strings.Repeat("x", 48), // não é hex válido
	} {
		w := httptest.NewRecorder()
		s.handleMFAVerify(w, httptest.NewRequest("POST", "/auth/mfa/verify",
			strings.NewReader(`{"challenge":"`+bad+`","code":"123456"}`)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("challenge %q: code=%d (queria 400)", bad, w.Code)
		}
	}
}

// TestHandleMFAVerifyMissingChallenge: formato válido mas ausente no Redis → 400
// (challengeUser devolve false sem incrementar o contador de tentativas).
func TestHandleMFAVerifyMissingChallenge(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	w := httptest.NewRecorder()
	s.handleMFAVerify(w, httptest.NewRequest("POST", "/auth/mfa/verify",
		strings.NewReader(`{"challenge":"`+strings.Repeat("b", 48)+`","code":"123456"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("challenge ausente no Redis: code=%d (queria 400)", w.Code)
	}
}

func TestHandleMFAEnableBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleMFAEnable(w, httptest.NewRequest("POST", "/auth/mfa/enable", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400)", w.Code)
	}
}

func TestHandleMFADisableBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleMFADisable(w, httptest.NewRequest("POST", "/auth/mfa/disable", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400)", w.Code)
	}
}

func TestHandleMFAEmailEnableBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleMFAEmailEnable(w, httptest.NewRequest("POST", "/auth/mfa/email-enable", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400)", w.Code)
	}
}

// TestHandleMFAEmailEnableTooManyAttempts: após 5 tentativas (pré-semeadas no Redis),
// a 6ª deve retornar 429 TOO_MANY_ATTEMPTS antes de qualquer consulta ao banco.
func TestHandleMFAEmailEnableTooManyAttempts(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	// Simula 5 tentativas anteriores para uid=42 diretamente no Redis
	if err := s.rdb.Set(context.Background(), "api-go:mfa_email_enable_attempts:42", "5", 0).Err(); err != nil {
		t.Fatalf("set attempt counter: %v", err)
	}
	w := httptest.NewRecorder()
	r := reqAs(httptest.NewRequest("POST", "/auth/mfa/email-enable",
		strings.NewReader(`{"code":"123456"}`)), 42)
	s.handleMFAEmailEnable(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("6ª tentativa: code=%d (queria 429)", w.Code)
	}
}

func TestHandleMFAMethodBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleMFAMethod(w, httptest.NewRequest("POST", "/auth/mfa/method", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400)", w.Code)
	}
}

// TestGenRecoveryCodesLen verifica que os códigos gerados têm exatamente
// recoveryCodeLen caracteres — mesma constante usada para filtrar a consulta ao banco.
func TestGenRecoveryCodesLen(t *testing.T) {
	codes := genRecoveryCodes(10)
	if len(codes) != 10 {
		t.Fatalf("queria 10 códigos, got %d", len(codes))
	}
	for i, c := range codes {
		if len(c) != recoveryCodeLen {
			t.Errorf("código[%d] = %q: comprimento %d (queria %d)", i, c, len(c), recoveryCodeLen)
		}
	}
}
