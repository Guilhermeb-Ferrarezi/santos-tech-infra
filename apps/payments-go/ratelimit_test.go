package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// reqFrom monta um request com peer TCP e headers de proxy dados.
func reqFrom(remoteAddr string, headers map[string]string) *http.Request {
	r := httptest.NewRequest("POST", "/link/tok/pay", nil)
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// TestClientIP_NaoConfiaEmXFFForjado é a regressão central: a implementação antiga
// pegava xff[0], mas o Traefik ANEXA ao X-Forwarded-For que o CLIENTE mandou. Logo
// xff[0] é escolhido pelo atacante — um valor novo a cada request zerava o
// rate-limit de /link/{token}/pay e escapava do ipBanCheck.
func TestClientIP_NaoConfiaEmXFFForjado(t *testing.T) {
	// Peer TCP público (não é proxy conhecido): headers são ignorados por completo.
	got := clientIP(reqFrom("203.0.113.9:4444", map[string]string{
		"X-Forwarded-For":  "1.2.3.4",
		"CF-Connecting-IP": "5.6.7.8",
	}))
	if got != "203.0.113.9" {
		t.Fatalf("peer não confiável deveria vencer os headers, veio %q", got)
	}

	// Peer é o Traefik (rede privada) e o cliente forjou o começo do XFF.
	// A lista real fica "<forjado>, <ip real do cliente>": o certo é o da direita.
	got = clientIP(reqFrom("10.0.0.5:4444", map[string]string{
		"X-Forwarded-For": "1.2.3.4, 198.51.100.7",
	}))
	if got != "198.51.100.7" {
		t.Fatalf("esperava o IP real (direita do XFF), veio %q", got)
	}
}

// TestClientIP_ProxyConfiavel cobre o caminho legítimo: atrás de Cloudflare +
// Traefik, o IP do cliente é respeitado.
func TestClientIP_ProxyConfiavel(t *testing.T) {
	got := clientIP(reqFrom("172.20.0.3:1234", map[string]string{
		"CF-Connecting-IP": "198.51.100.42",
		"X-Forwarded-For":  "lixo, 198.51.100.42, 172.20.0.2",
	}))
	if got != "198.51.100.42" {
		t.Fatalf("CF-Connecting-IP deveria valer atrás de proxy confiável, veio %q", got)
	}

	// XFF só com proxies confiáveis → cai no peer, nunca num valor forjado.
	got = clientIP(reqFrom("10.1.2.3:1234", map[string]string{
		"X-Forwarded-For": "10.0.0.9, 172.16.0.4",
	}))
	if got != "10.1.2.3" {
		t.Fatalf("sem candidato não-confiável deveria cair no peer, veio %q", got)
	}

	// Sem header nenhum: peer.
	if got := clientIP(reqFrom("10.1.2.3:1234", nil)); got != "10.1.2.3" {
		t.Fatalf("sem headers deveria usar o peer, veio %q", got)
	}
}

// TestPayLinkRateLimit_NaoEhBurlavelPorXFF: dois requests com XFF diferentes, mesmo
// peer não-confiável, compartilham o MESMO bucket.
func TestPayLinkRateLimit_NaoEhBurlavelPorXFF(t *testing.T) {
	reg := newIPLimiterRegistry(rate.Every(time.Hour), 1, 5*time.Minute)

	ip1 := clientIP(reqFrom("203.0.113.77:1111", map[string]string{"X-Forwarded-For": "9.9.9.1"}))
	ip2 := clientIP(reqFrom("203.0.113.77:2222", map[string]string{"X-Forwarded-For": "9.9.9.2"}))
	if ip1 != ip2 {
		t.Fatalf("o mesmo atacante recebeu chaves diferentes (%q vs %q)", ip1, ip2)
	}
	if !reg.For(ip1).Allow() {
		t.Fatal("o 1º request deveria passar")
	}
	if reg.For(ip2).Allow() {
		t.Fatal("o 2º request, só com o XFF trocado, não deveria passar")
	}
}

// TestIPLimiterRegistry_Sweep: os buckets ociosos saem do mapa (a limpeza agora é
// um ticker de fundo, não uma varredura do mapa inteiro a cada request).
func TestIPLimiterRegistry_Sweep(t *testing.T) {
	reg := newIPLimiterRegistry(rate.Every(time.Second), 1, time.Minute)
	reg.For("1.1.1.1")
	reg.For("2.2.2.2")
	reg.mu.Lock()
	n := len(reg.entries)
	reg.mu.Unlock()
	if n != 2 {
		t.Fatalf("esperava 2 buckets, veio %d", n)
	}

	reg.sweep(time.Now()) // nada ocioso ainda
	reg.mu.Lock()
	n = len(reg.entries)
	reg.mu.Unlock()
	if n != 2 {
		t.Fatalf("sweep não deveria remover buckets recentes, sobraram %d", n)
	}

	reg.sweep(time.Now().Add(2 * time.Minute))
	reg.mu.Lock()
	n = len(reg.entries)
	reg.mu.Unlock()
	if n != 0 {
		t.Fatalf("sweep deveria ter limpado os buckets ociosos, sobraram %d", n)
	}
}
