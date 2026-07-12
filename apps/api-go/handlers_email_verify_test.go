package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// Cobre os caminhos de validação do confirm que retornam ANTES de tocar no Redis/banco.
func TestEmailVerifyConfirmValidation(t *testing.T) {
	s := testServer(Config{})

	// corpo inválido → 400
	w := httptest.NewRecorder()
	s.handleEmailVerifyConfirm(w, httptest.NewRequest("POST", "/auth/email-verify/confirm", strings.NewReader("xx")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido deveria ser 400, veio %d", w.Code)
	}

	// código curto → 400 (sem consultar o Redis)
	w2 := httptest.NewRecorder()
	s.handleEmailVerifyConfirm(w2, httptest.NewRequest("POST", "/auth/email-verify/confirm", strings.NewReader(`{"code":"123"}`)))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("código curto deveria ser 400, veio %d", w2.Code)
	}

	// código longo → 400
	w3 := httptest.NewRecorder()
	s.handleEmailVerifyConfirm(w3, httptest.NewRequest("POST", "/auth/email-verify/confirm", strings.NewReader(`{"code":"1234567"}`)))
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("código longo deveria ser 400, veio %d", w3.Code)
	}
}

// TestEmailVerifyConfirmRedisDown garante que uma falha do Redis retorna 500
// (fail-closed), e não um enganoso 400 CODE_EXPIRED. Sem isso, o cliente poderia
// interpretar "código expirado" como instrução para pedir um novo código, quando
// o código original ainda pode estar válido e o problema é de infraestrutura.
func TestEmailVerifyConfirmRedisDown(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })

	mr.SetError("ERR simulated Redis failure")

	w := httptest.NewRecorder()
	r := reqAs(httptest.NewRequest("POST", "/auth/email-verify/confirm",
		strings.NewReader(`{"code":"123456"}`)), 42)
	s.handleEmailVerifyConfirm(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("Redis indisponível deve retornar 500 (fail-closed), veio %d", w.Code)
	}
}

// TestEmailVerifyConfirmCodeExpired garante que a ausência do código no Redis
// (chave inexistente) retorna 400 CODE_EXPIRED — e não 500.
func TestEmailVerifyConfirmCodeExpired(t *testing.T) {
	s := testServerWithRedis(t, Config{})

	w := httptest.NewRecorder()
	r := reqAs(httptest.NewRequest("POST", "/auth/email-verify/confirm",
		strings.NewReader(`{"code":"123456"}`)), 42)
	s.handleEmailVerifyConfirm(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("código ausente no Redis deve retornar 400, veio %d", w.Code)
	}
	// Confirma que foi CODE_EXPIRED, não algum outro 400
	body := w.Body.String()
	if !strings.Contains(body, "CODE_EXPIRED") {
		t.Errorf("esperava CODE_EXPIRED no corpo, veio: %s", body)
	}
}

// TestEmailVerifyConfirmCodeValidHitsDB garante que um código correto no Redis não
// retorna CODE_EXPIRED — valida que o caminho Redis foi percorrido corretamente.
// Sem banco real, o handler pânica ou retorna 500; ambos provam que o Redis foi
// consumido com sucesso (o erro não é "código expirado").
func TestEmailVerifyConfirmCodeValidHitsDB(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })

	// Insere o código diretamente no Redis fake (simula código enviado ao usuário).
	if err := s.rdb.Set(context.Background(), emailVerifyKey(42), "654321", emailVerifyTTL).Err(); err != nil {
		t.Fatalf("set código: %v", err)
	}

	var gotCode int
	gotPanic := false
	func() {
		defer func() {
			if recover() != nil {
				gotPanic = true // nil DB → pânica ao chamar setEmailVerified; esperado
			}
		}()
		w := httptest.NewRecorder()
		r := reqAs(httptest.NewRequest("POST", "/auth/email-verify/confirm",
			strings.NewReader(`{"code":"654321"}`)), 42)
		s.handleEmailVerifyConfirm(w, r)
		gotCode = w.Code
	}()
	// Pânica (nil DB) ou 500 ambos provam que chegamos ao banco sem retornar CODE_EXPIRED.
	if !gotPanic && gotCode == http.StatusBadRequest {
		t.Error("código correto não deveria retornar 400 (CODE_EXPIRED)")
	}
}
