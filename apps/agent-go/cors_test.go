package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// O account-kit manda X-Requested-With em toda mutação SEM corpo (ex.: POST
// /claude/designs/{id}/preview-token, DELETE /claude/conversations/{id}). Sem
// ele no Allow-Headers o browser barra o preflight e o fetch rejeita com
// "Failed to fetch" — o preview do Claude Design nunca carregava por isso.
func TestCORSPreflightAllowsXRequestedWith(t *testing.T) {
	s := &Server{cfg: Config{CORSOrigins: []string{"https://santos-tech.com"}, Production: true}}
	h := s.cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("preflight não deveria chegar no handler")
	}))

	req := httptest.NewRequest(http.MethodOptions, "/claude/designs/x/preview-token", nil)
	req.Header.Set("Origin", "https://santos-tech.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "x-requested-with")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	allowed := strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers"))
	if !strings.Contains(allowed, "x-requested-with") {
		t.Fatalf("Access-Control-Allow-Headers = %q, falta X-Requested-With", rec.Header().Get("Access-Control-Allow-Headers"))
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://santos-tech.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}
