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

// TestHandleResetPasswordTokenAbsent garante que um token com formato válido mas
// ausente no Redis devolve 400 INVALID_TOKEN — e não 500.
func TestHandleResetPasswordTokenAbsent(t *testing.T) {
	s := testServerWithRedis(t, Config{})

	validToken := strings.Repeat("a", 64)
	w := httptest.NewRecorder()
	s.handleResetPassword(w, httptest.NewRequest("POST", "/auth/reset-password",
		strings.NewReader(`{"token":"`+validToken+`","newPassword":"senha1234"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("token ausente no Redis: code=%d (queria 400)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "INVALID_TOKEN") {
		t.Errorf("esperava INVALID_TOKEN no corpo, veio: %s", w.Body.String())
	}
}

// TestHandleResetPasswordRedisDown garante que uma falha do Redis retorna 500
// (fail-closed), e não um enganoso 400 INVALID_TOKEN que poderia levar o usuário
// a solicitar um novo link quando o link original ainda é válido.
func TestHandleResetPasswordRedisDown(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })

	mr.SetError("ERR simulated Redis failure")

	validToken := strings.Repeat("a", 64)
	w := httptest.NewRecorder()
	s.handleResetPassword(w, httptest.NewRequest("POST", "/auth/reset-password",
		strings.NewReader(`{"token":"`+validToken+`","newPassword":"senha1234"}`)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("Redis indisponível deve retornar 500 (fail-closed), veio %d", w.Code)
	}
}

// TestHandleResetPasswordTokenHitsRedis garante que um token correto no Redis
// avança além da camada Redis (erro do banco ou pânica por nil pool) —
// provando que GetDel foi chamado e consumiu a chave.
func TestHandleResetPasswordTokenHitsRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })

	rawToken := strings.Repeat("b", 64)
	hash := sha256Hex(rawToken)
	if err := s.rdb.Set(context.Background(), "pwd_reset:"+hash, "99", 0).Err(); err != nil {
		t.Fatalf("set token: %v", err)
	}

	var gotCode int
	gotPanic := false
	func() {
		defer func() {
			if recover() != nil {
				gotPanic = true // nil pool → pânica ao chamar userByID; esperado
			}
		}()
		w := httptest.NewRecorder()
		s.handleResetPassword(w, httptest.NewRequest("POST", "/auth/reset-password",
			strings.NewReader(`{"token":"`+rawToken+`","newPassword":"senha1234"}`)))
		gotCode = w.Code
	}()
	// Pânica (nil pool) ou 500 do banco provam que chegamos além do Redis sem
	// retornar INVALID_TOKEN.
	if !gotPanic && gotCode == http.StatusBadRequest {
		t.Error("token correto não deveria retornar 400 (INVALID_TOKEN)")
	}

	// A chave deve ter sido deletada do Redis (GetDel é atômico).
	exists, err := s.rdb.Exists(context.Background(), "pwd_reset:"+hash).Result()
	if err != nil {
		t.Fatalf("redis exists: %v", err)
	}
	if exists != 0 {
		t.Error("GetDel deve ter removido a chave do Redis")
	}
}
