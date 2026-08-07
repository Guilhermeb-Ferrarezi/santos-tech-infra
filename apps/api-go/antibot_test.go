package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestAntiBotHoneypotBansImmediately(t *testing.T) {
	s := testServerWithRedis(t, Config{})

	called := false
	h := s.antiBotCheck(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/.env", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if called {
		t.Fatal("handler não deveria ser chamado para path de honeypot")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("code=%d (queria 404, igual a um 404 normal — não deve denunciar a armadilha)", w.Code)
	}

	n, err := s.rdb.Exists(context.Background(), "global:ip-ban:203.0.113.9").Result()
	if err != nil || n == 0 {
		t.Fatal("IP deveria estar banido no Redis após hit em honeypot")
	}
}

func TestAntiBotBadUABans(t *testing.T) {
	s := testServerWithRedis(t, Config{})

	called := false
	h := s.antiBotCheck(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/auth/me", nil)
	r.RemoteAddr = "203.0.113.10:1234"
	r.Header.Set("User-Agent", "vuln_scanner/3.1.0 (CVE-2026-4020)")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if called {
		t.Fatal("handler não deveria ser chamado para UA de scanner conhecido")
	}
	n, err := s.rdb.Exists(context.Background(), "global:ip-ban:203.0.113.10").Result()
	if err != nil || n == 0 {
		t.Fatal("IP deveria estar banido no Redis após UA de scanner")
	}
}

func TestAntiBotAllowsLegitimateTraffic(t *testing.T) {
	s := testServerWithRedis(t, Config{})

	called := false
	h := s.antiBotCheck(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("POST", "/oauth/token", nil)
	r.RemoteAddr = "203.0.113.11:1234"
	r.Header.Set("User-Agent", "python-httpx/0.28.1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if !called {
		t.Fatal("tráfego legítimo (rota real, UA de lib comum) não deveria ser bloqueado")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d (queria 200)", w.Code)
	}
	n, _ := s.rdb.Exists(context.Background(), "global:ip-ban:203.0.113.11").Result()
	if n != 0 {
		t.Fatal("tráfego legítimo não deveria gerar ban")
	}
}

func TestAntiBot404BurstBans(t *testing.T) {
	s := testServerWithRedis(t, Config{})

	h := s.antiBotCheck(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))

	ip := "203.0.113.12"
	for i := 0; i < notFoundBurstMax; i++ {
		r := httptest.NewRequest("GET", "/random-path-nao-existe", nil)
		r.RemoteAddr = ip + ":1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("request %d: code=%d (queria 404)", i, w.Code)
		}
	}

	// Ainda não deveria estar banido — está exatamente no limite, não acima dele.
	n, _ := s.rdb.Exists(context.Background(), "global:ip-ban:"+ip).Result()
	if n != 0 {
		t.Fatal("não deveria banir no exato limiar, só ao ultrapassá-lo")
	}

	// Mais um 404 estoura o limiar.
	r := httptest.NewRequest("GET", "/outro-path-nao-existe", nil)
	r.RemoteAddr = ip + ":1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	n, err := s.rdb.Exists(context.Background(), "global:ip-ban:"+ip).Result()
	if err != nil || n == 0 {
		t.Fatal("IP deveria estar banido após ultrapassar o limiar de burst de 404")
	}
}

func TestAntiBot404BurstFailOpenOnRedisDown(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })

	h := s.antiBotCheck(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))

	mr.SetError("ERR simulated Redis failure")

	r := httptest.NewRequest("GET", "/nao-existe", nil)
	r.RemoteAddr = "203.0.113.13:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r) // não deve travar nem panicar mesmo com Redis fora

	if w.Code != http.StatusNotFound {
		t.Fatalf("code=%d (queria 404 mesmo com Redis fora — resposta já foi decidida pelo handler)", w.Code)
	}
}

func TestAntiBotSkipsOperationalEndpoints(t *testing.T) {
	s := testServerWithRedis(t, Config{})

	called := false
	h := s.antiBotCheck(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"/health", "/ready", "/metrics"} {
		called = false
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !called {
			t.Errorf("%s deveria pular o antibot (probe/scrape frequente)", path)
		}
	}
}

func TestIsBadUA(t *testing.T) {
	cases := []struct {
		ua   string
		want bool
	}{
		{"vuln_scanner/3.1.0 (CVE-2026-4020)", true},
		{"sqlmap/1.7.2", true},
		{"Mozilla/5.0 (compatible; Nmap Scripting Engine)", true},
		{"python-httpx/0.28.1", false},
		{"Mozilla/5.0 (compatible; Diffbot/1.0; +https://diffbot.com)", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isBadUA(c.ua); got != c.want {
			t.Errorf("isBadUA(%q) = %v, want %v", c.ua, got, c.want)
		}
	}
}

func TestHoneypotPathsDontCollideWithRealRoutes(t *testing.T) {
	// Guarda-de-sanidade: nenhuma rota real registrada pode virar honeypot por
	// engano (banaria usuários legítimos). Amostra das rotas públicas/comuns.
	real := []string{
		"/health", "/ready", "/metrics", "/auth/login", "/auth/me",
		"/oauth/token", "/oauth/authorize", "/public/blog/posts",
		"/.well-known/oauth-authorization-server", "/.well-known/jwks.json",
		"/status", "/llms.txt", "/boards",
	}
	for _, p := range real {
		if honeypotPaths[p] {
			t.Errorf("rota real %q está marcada como honeypot", p)
		}
	}
}
