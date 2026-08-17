package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
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

func TestHandleLoginValidation(t *testing.T) {
	s := testServer(Config{})

	// identifier vazio → 401 antes do banco/Redis
	w := httptest.NewRecorder()
	s.handleLogin(w, httptest.NewRequest("POST", "/auth/login",
		strings.NewReader(`{"identifier":"","password":"senha123"}`)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("identifier vazio: code=%d (queria 401)", w.Code)
	}

	// identifier muito longo (>254 chars) → 401 antes do banco/Redis
	w2 := httptest.NewRecorder()
	s.handleLogin(w2, httptest.NewRequest("POST", "/auth/login",
		strings.NewReader(`{"identifier":"`+strings.Repeat("a", 255)+`@b.com","password":"senha123"}`)))
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("identifier longo: code=%d (queria 401)", w2.Code)
	}

	// password muito longa (>128 chars) → 401 antes do argon2id (evita CPU-DoS)
	w3 := httptest.NewRecorder()
	s.handleLogin(w3, httptest.NewRequest("POST", "/auth/login",
		strings.NewReader(`{"identifier":"a@b.com","password":"`+strings.Repeat("a", 129)+`"}`)))
	if w3.Code != http.StatusUnauthorized {
		t.Fatalf("password longa: code=%d (queria 401)", w3.Code)
	}
}

// TestDummyPasswordHashValid verifica que o hash sentinela de timing (usado para
// normalizar o tempo de resposta do login quando o usuário não existe) é gerado
// corretamente no boot e pode ser verificado pelo argon2id.
func TestDummyPasswordHashValid(t *testing.T) {
	if dummyPasswordHash == "" {
		t.Fatal("dummyPasswordHash vazio: verifyPassword seria um no-op para usuários inexistentes")
	}
	// verifyPassword com a senha correta deve retornar true — confirma que o hash
	// sentinela é um argon2id bem formado e que a normalização de timing funcionaria.
	if !verifyPassword("__timing_sentinel__", dummyPasswordHash) {
		t.Fatal("verifyPassword falhou contra dummyPasswordHash: hash inválido")
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

func TestHandleForgotPasswordValidation(t *testing.T) {
	s := testServer(Config{})

	// corpo inválido → 400 antes de qualquer I/O
	w := httptest.NewRecorder()
	s.handleForgotPassword(w, httptest.NewRequest("POST", "/auth/forgot-password", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido: code=%d", w.Code)
	}

	// email malformado → 200 "ok" sem tocar no banco
	for _, bad := range []string{"", "notanemail", "@nodomain.com", "missing@tld", "spaces in@email.com"} {
		w2 := httptest.NewRecorder()
		s.handleForgotPassword(w2, httptest.NewRequest("POST", "/auth/forgot-password",
			strings.NewReader(`{"email":"`+bad+`"}`)))
		if w2.Code != http.StatusOK {
			t.Errorf("email inválido %q: code=%d (queria 200)", bad, w2.Code)
		}
	}
}

// TestHandleLoginRedisDown garante que o lockout de login retorna 503
// (fail-closed) quando o Redis está indisponível — impede que brute-force
// seja possível durante quedas do Redis.
func TestHandleLoginRedisDown(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })

	mr.SetError("ERR simulated Redis failure")

	w := httptest.NewRecorder()
	s.handleLogin(w, httptest.NewRequest("POST", "/auth/login",
		strings.NewReader(`{"identifier":"user@example.com","password":"senha123"}`)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("Redis indisponível deve retornar 503 (fail-closed), veio %d", w.Code)
	}
}

// TestWriteErrWrappedAppError garante que writeErr unwraps corretamente um
// *AppError embrulhado com fmt.Errorf("%w", ...) — sem errors.As, erros
// embrulhados cairiam no branch genérico de 500.
func TestWriteErrWrappedAppError(t *testing.T) {
	inner := appErr(http.StatusNotFound, "NOT_FOUND", "recurso não encontrado")
	wrapped := fmt.Errorf("camada de serviço: %w", inner)

	w := httptest.NewRecorder()
	writeErr(w, wrapped)

	if w.Code != http.StatusNotFound {
		t.Fatalf("AppError embrulhado deve preservar status %d, veio %d", http.StatusNotFound, w.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("resposta não é JSON válido: %v", err)
	}
	if body["code"] != "NOT_FOUND" {
		t.Errorf("code esperado NOT_FOUND, veio %q", body["code"])
	}
}
