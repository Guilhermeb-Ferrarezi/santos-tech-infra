package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// O dashboard (account-kit/client.ts) manda X-Requested-With como sinal
// anti-CSRF em toda mutação sem corpo; se o preflight não liberar o header,
// o browser bloqueia a request antes de ela sair.
func TestCORSLiberaXRequestedWith(t *testing.T) {
	s := &Server{cfg: Config{DashCORSOrigin: "https://santos-tech.com"}}
	w := httptest.NewRecorder()
	s.setCORSHeaders(w)

	got := w.Header().Get("Access-Control-Allow-Headers")
	for _, h := range []string{"Content-Type", "X-Dash-Key", "Authorization", "X-Requested-With"} {
		if !strings.Contains(got, h) {
			t.Errorf("Allow-Headers=%q não libera %s", got, h)
		}
	}
	if o := w.Header().Get("Access-Control-Allow-Origin"); o != "https://santos-tech.com" {
		t.Errorf("Allow-Origin=%q, quer só a origem configurada", o)
	}
}

// Allowlist continua fail-closed: sem origem configurada, nenhum header CORS.
func TestCORSSemOrigemNaoLiberaNada(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.setCORSHeaders(w)
	if len(w.Header()) != 0 {
		t.Fatalf("sem DashCORSOrigin não deveria setar header CORS: %v", w.Header())
	}
}
