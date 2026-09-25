package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
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

// TestAntiBot404BurstIgnoraPollingPublico: 404 em rota pública de polling é
// resposta de API esperada (sessão de horas apagada, PC não cadastrado), não
// varredura. Com NAT, UMA aba/app esquecido consultando sessão morta a cada 2s
// baniu o IP da escola inteira (25/09/2026) e derrubou heartbeat e cronômetro
// de todos os PCs. Essas rotas já têm rateLimit próprio por IP.
func TestAntiBot404BurstIgnoraPollingPublico(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	h := s.antiBotCheck(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	ip := "203.0.113.50"
	paths := []string{
		"/public/hour-sessions/973f3aabb9830c56f07a097e4517927d42ac41a8c789c66b6d63ebd5af8b43cb",
		"/public/lab-devices/heartbeat",
	}
	for i := 0; i < notFoundBurstMax*3; i++ {
		r := httptest.NewRequest("GET", paths[i%len(paths)], nil)
		r.RemoteAddr = ip + ":1234"
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	if n, _ := s.rdb.Exists(context.Background(), "global:ip-ban:"+ip).Result(); n != 0 {
		t.Fatal("404 em polling público não pode banir o IP")
	}
	// E uma varredura de verdade no mesmo IP continua sendo contada.
	for i := 0; i <= notFoundBurstMax; i++ {
		r := httptest.NewRequest("GET", "/caminho-inexistente-qualquer", nil)
		r.RemoteAddr = ip + ":1234"
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	if n, _ := s.rdb.Exists(context.Background(), "global:ip-ban:"+ip).Result(); n == 0 {
		t.Fatal("varredura fora do polling público ainda deveria banir")
	}
}

// TestAntiBot404BurstVarreduraSobPrefixoPublicoAindaBane: a exceção do polling
// público vale só pro FORMATO real das rotas (token de 64 hex, rotas exatas de
// lab-devices). Um scanner varrendo caminhos inventados debaixo desses
// prefixos (/public/hour-sessions/.env, /public/lab-devices/config.json) não
// pode herdar a imunidade — senão a correção do incidente de 25/09/2026 vira
// um buraco na detecção de varredura.
func TestAntiBot404BurstVarreduraSobPrefixoPublicoAindaBane(t *testing.T) {
	for _, prefix := range []string{"/public/hour-sessions/", "/public/lab-devices/"} {
		s := testServerWithRedis(t, Config{})
		h := s.antiBotCheck(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		ip := "203.0.113.51"
		for i := 0; i <= notFoundBurstMax; i++ {
			r := httptest.NewRequest("GET", fmt.Sprintf("%sscan-%d.php", prefix, i), nil)
			r.RemoteAddr = ip + ":1234"
			h.ServeHTTP(httptest.NewRecorder(), r)
		}
		if n, _ := s.rdb.Exists(context.Background(), "global:ip-ban:"+ip).Result(); n == 0 {
			t.Fatalf("varredura de caminho inventado sob %s deveria banir", prefix)
		}
	}
}

// TestIsPublicPollingPath fixa o formato exato que fica fora do burst de 404.
func TestIsPublicPollingPath(t *testing.T) {
	tok := strings.Repeat("ab", 32)
	cases := map[string]bool{
		"/public/hour-sessions/" + tok:                      true,
		"/public/hour-sessions/" + tok + "/request-pause":   true,
		"/public/hour-sessions/" + tok + "/request-end":     true,
		"/public/lab-devices/heartbeat":                     true,
		"/public/lab-devices/wait-command":                  true,
		"/public/lab-devices/command-result":                true,
		"/public/hour-sessions/.env":                        false,
		"/public/hour-sessions/" + tok + "/.env":            false,
		"/public/hour-sessions/" + tok[:63]:                 false,
		"/public/hour-sessions/" + strings.Repeat("zz", 32): false,
		"/public/hour-sessions/pair-by-code":                false,
		"/public/lab-devices/config.json":                   false,
		"/public/lab-devices/heartbeat/x":                   false,
		"/caminho-qualquer":                                 false,
	}
	for path, want := range cases {
		if got := isPublicPollingPath(path); got != want {
			t.Errorf("isPublicPollingPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestPublicLabDeviceRoutesEmSincroniaComRoutes: toda rota pública de
// /public/lab-devices/ registrada em routes.go precisa estar em
// publicLabDeviceRoutes — rota nova fora do mapa voltaria a contar 404 pro
// burst e poderia banir o IP da escola inteira de novo.
func TestPublicLabDeviceRoutesEmSincroniaComRoutes(t *testing.T) {
	src, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`"[A-Z]+ /public/lab-devices/([^"\s]+)"`)
	found := re.FindAllStringSubmatch(string(src), -1)
	if len(found) == 0 {
		t.Fatal("nenhuma rota /public/lab-devices/ encontrada em routes.go — regex desatualizada?")
	}
	for _, m := range found {
		if !publicLabDeviceRoutes[m[1]] {
			t.Errorf("rota /public/lab-devices/%s falta em publicLabDeviceRoutes (antibot.go)", m[1])
		}
	}
}
