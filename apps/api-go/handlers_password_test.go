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

// TestHandleResetPasswordTokenNotInRedis: token de formato válido mas ausente no
// Redis deve retornar 400 INVALID_TOKEN — não um erro de infraestrutura.
func TestHandleResetPasswordTokenNotInRedis(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	validToken := strings.Repeat("a", 64)
	w := httptest.NewRecorder()
	s.handleResetPassword(w, httptest.NewRequest("POST", "/auth/reset-password",
		strings.NewReader(`{"token":"`+validToken+`","newPassword":"senha1234"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("token ausente no Redis: code=%d (queria 400)", w.Code)
	}
}

// TestHandleResetPasswordRedisDown garante que handleResetPassword retorna 500
// (fail-closed) quando o Redis está indisponível. O handler consome o token via
// GetDel — falha de conexão deve ser um erro de infraestrutura (500), não ser
// silenciado como "token inválido" (400), o que dificultaria o diagnóstico e
// poderia mascarar uma indisponibilidade do Redis em produção.
func TestHandleResetPasswordRedisDown(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })

	// Pré-semeamos uma chave válida para que o handler alcance o GetDel.
	validToken := strings.Repeat("a", 64)
	hash := sha256Hex(validToken)
	if err := s.rdb.Set(context.Background(), "pwd_reset:"+hash, "42", 0).Err(); err != nil {
		t.Fatalf("set token: %v", err)
	}

	mr.SetError("ERR simulated Redis failure")

	w := httptest.NewRecorder()
	s.handleResetPassword(w, httptest.NewRequest("POST", "/auth/reset-password",
		strings.NewReader(`{"token":"`+validToken+`","newPassword":"senha1234"}`)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("Redis indisponível deve retornar 500 (fail-closed), veio %d", w.Code)
	}
}
