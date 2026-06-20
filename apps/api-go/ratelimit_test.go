package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestRateLimitUsesPattern garante que rotas com path params compartilham o
// mesmo balde de rate limit. Sem isso, ciclar IDs (/boards/1, /boards/2, …)
// contornaria o limite por IP.
func TestRateLimitUsesPattern(t *testing.T) {
	s := testServerWithRedis(t, Config{})

	calls := 0
	h := s.rateLimit(2, time.Minute, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", h)

	// Duas chamadas a /items/1 esgotam o balde.
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/items/1", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("request %d a /items/1: code=%d (queria 200)", i+1, w.Code)
		}
	}

	// /items/2 (ID diferente, mesmo padrão) deve ser bloqueado pelo mesmo balde.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/items/2", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("/items/2 deveria ser bloqueado (mesmo balde de padrão): code=%d", w.Code)
	}
	if calls != 2 {
		t.Fatalf("handler chamado %d vezes (esperava 2)", calls)
	}
}

func TestClientIP(t *testing.T) {
	mk := func(remote string, hdr map[string]string) *http.Request {
		r := &http.Request{RemoteAddr: remote, Header: http.Header{}}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		name string
		req  *http.Request
		want string
	}{
		{"cloudflare", mk("10.0.0.1:1234", map[string]string{"CF-Connecting-IP": "203.0.113.7"}), "203.0.113.7"},
		{"x-forwarded-for", mk("10.0.0.1:1234", map[string]string{"X-Forwarded-For": "198.51.100.2, 10.0.0.1"}), "198.51.100.2"},
		{"remote-addr", mk("192.0.2.5:9999", nil), "192.0.2.5"},
		{"cf-tem-prioridade", mk("10.0.0.1:1", map[string]string{"CF-Connecting-IP": "1.1.1.1", "X-Forwarded-For": "2.2.2.2"}), "1.1.1.1"},
		{"remote-sem-porta", mk("192.0.2.9", nil), "192.0.2.9"},
	}
	for _, c := range cases {
		if got := clientIP(c.req); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// TestRateLimitFailClosed verifica que rateLimit (aplicado em rotas sensíveis:
// login, forgot-password, MFA) bloqueia quando o Redis está indisponível.
// Fail-closed é intencional: prefere negar temporariamente a abrir o portão de
// brute-force durante uma queda do Redis.
func TestRateLimitFailClosed(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })

	called := false
	h := s.rateLimit(100, time.Minute, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", h)

	mr.SetError("ERR simulated Redis failure")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/auth/login", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("rateLimit com Redis falhando deve bloquear (429), got %d", w.Code)
	}
	if called {
		t.Fatal("handler não deve ser chamado quando rateLimit está fail-closed e Redis falhou")
	}
}

// TestGlobalRateLimitFailOpen verifica que globalRateLimit (camada mais externa,
// todas as rotas) deixa passar quando o Redis está indisponível. Fail-open é
// intencional: evita derrubar o site todo durante queda do Redis; as rotas
// sensíveis têm seu próprio rateLimit fail-closed por cima.
func TestGlobalRateLimitFailOpen(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })

	called := false
	h := s.globalRateLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	mr.SetError("ERR simulated Redis failure")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/auth/me", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("globalRateLimit com Redis falhando deve deixar passar (200), got %d", w.Code)
	}
	if !called {
		t.Fatal("handler deve ser chamado quando globalRateLimit é fail-open e Redis falhou")
	}
}
