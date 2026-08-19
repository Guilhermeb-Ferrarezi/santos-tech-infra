package main

import (
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ── IP real do cliente ───────────────────────────────────────────────────────
// Portado de apps/api-go/ratelimit.go. A implementação anterior aqui pegava o
// PRIMEIRO IP do X-Forwarded-For, que é justamente o pedaço forjável: o Traefik
// ANEXA ao XFF que o cliente mandou, então bastava enviar um X-Forwarded-For
// aleatório a cada request para zerar o rate-limit de /link/{token}/pay e
// escapar do ipBanCheck.

// trustedProxyNets é a allowlist de redes (CIDRs) de quem pode falar por nós via
// CF-Connecting-IP / X-Forwarded-For. Só confiamos nesses headers quando o
// RemoteAddr (o peer TCP real) cai aqui dentro. Configurável por
// TRUSTED_PROXY_CIDRS (CSV); senão usa os ranges publicados da Cloudflare +
// redes privadas (onde o Traefik roda).
var (
	trustedProxyNets []*net.IPNet
	trustedProxyOnce sync.Once
)

// defaultTrustedProxyCIDRs: ranges da Cloudflare (https://www.cloudflare.com/ips/)
// + privados/loopback. O Traefik fica na rede docker/host privada, então a borda
// que nos entrega o XFF está sempre dentro de um destes.
var defaultTrustedProxyCIDRs = []string{
	// Cloudflare IPv4
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	// Cloudflare IPv6
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
	// Redes privadas / loopback (Traefik / docker / host)
	"127.0.0.0/8", "::1/128",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"fc00::/7",
}

// loadTrustedProxies carrega a allowlist uma única vez.
func loadTrustedProxies() []*net.IPNet {
	trustedProxyOnce.Do(func() {
		cidrs := defaultTrustedProxyCIDRs
		if env := strings.TrimSpace(os.Getenv("TRUSTED_PROXY_CIDRS")); env != "" {
			cidrs = nil
			for _, part := range strings.Split(env, ",") {
				if c := strings.TrimSpace(part); c != "" {
					cidrs = append(cidrs, c)
				}
			}
		}
		for _, c := range cidrs {
			if _, n, err := net.ParseCIDR(c); err == nil {
				trustedProxyNets = append(trustedProxyNets, n)
			}
		}
	})
	return trustedProxyNets
}

// isTrustedProxy diz se o IP (peer TCP) está na allowlist de proxies.
func isTrustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range loadTrustedProxies() {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// remoteIP devolve o IP do peer TCP (RemoteAddr), sem porta.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clientIP extrai o IP real do cliente (atrás de Cloudflare + Traefik), mas SÓ
// confia nos headers de proxy quando o peer TCP (RemoteAddr) é um proxy conhecido.
// Se não for confiável, usa o próprio RemoteAddr — assim um cliente direto não
// consegue forjar o IP para escapar do rate-limit ou do ban.
func clientIP(r *http.Request) string {
	peer := remoteIP(r)
	peerIP := net.ParseIP(peer)

	// Peer não confiável → ignora qualquer header e usa o IP real da conexão.
	if !isTrustedProxy(peerIP) {
		return peer
	}

	// CF-Connecting-IP: a Cloudflare injeta o IP original do cliente. Só vale
	// quem está atrás dela; como o peer é confiável, podemos usá-lo.
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
		if net.ParseIP(ip) != nil {
			return ip
		}
	}

	// X-Forwarded-For: lista "cliente, proxy1, proxy2, ...". Como cada hop ANEXA
	// o IP de quem o chamou, os da DIREITA são os mais confiáveis. Andamos da
	// direita pra esquerda pulando os IPs que são proxies confiáveis; o primeiro
	// IP NÃO-confiável é o cliente real. Pegar o [0] (esquerda) seria forjável.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			cand := strings.TrimSpace(parts[i])
			candIP := net.ParseIP(cand)
			if candIP == nil {
				continue
			}
			if !isTrustedProxy(candIP) {
				return cand
			}
		}
	}

	// Sem header utilizável: cai no peer (que é o próprio proxy confiável).
	return peer
}

// ── Buckets de rate-limit por IP ─────────────────────────────────────────────
// Process-level (in-memory); em deploy com múltiplas instâncias, combine com um
// limitador distribuído via Redis.

type ipLimiterEntry struct {
	lim     *rate.Limiter
	lastUse time.Time
}

// ipLimiterRegistry guarda um bucket por IP. A limpeza roda num ticker de fundo:
// a versão anterior varria o mapa INTEIRO sob mutex a cada request, o que fazia
// do próprio rate-limiter um amplificador de carga justamente sob ataque.
type ipLimiterRegistry struct {
	mu      sync.Mutex
	entries map[string]*ipLimiterEntry
	every   rate.Limit
	burst   int
	idleTTL time.Duration
}

func newIPLimiterRegistry(every rate.Limit, burst int, idleTTL time.Duration) *ipLimiterRegistry {
	reg := &ipLimiterRegistry{
		entries: make(map[string]*ipLimiterEntry),
		every:   every,
		burst:   burst,
		idleTTL: idleTTL,
	}
	go reg.janitor(idleTTL)
	return reg
}

// For devolve (ou cria) o bucket do IP. O(1) — sem varredura do mapa.
func (reg *ipLimiterRegistry) For(ip string) *rate.Limiter {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	e, ok := reg.entries[ip]
	if !ok {
		e = &ipLimiterEntry{lim: rate.NewLimiter(reg.every, reg.burst)}
		reg.entries[ip] = e
	}
	e.lastUse = time.Now()
	return e.lim
}

// sweep remove os buckets ociosos. Separado do janitor para ser testável.
func (reg *ipLimiterRegistry) sweep(now time.Time) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	for k, e := range reg.entries {
		if now.Sub(e.lastUse) > reg.idleTTL {
			delete(reg.entries, k)
		}
	}
}

func (reg *ipLimiterRegistry) janitor(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for now := range t.C {
		reg.sweep(now)
	}
}

// payLinkLimiters: POST /link/{token}/pay — 5 req/min por IP, burst 5.
var payLinkLimiters = newIPLimiterRegistry(rate.Every(time.Minute/5), 5, 5*time.Minute)

// payLinkLimiterFor devolve o rate-limiter associado ao IP dado.
func payLinkLimiterFor(ip string) *rate.Limiter { return payLinkLimiters.For(ip) }

// ipBanCheck bloqueia IPs banidos consultando Redis ("global:ip-ban:<ip>").
// Fail-open: Redis indisponível → passa. Endpoints operacionais são isentos.
func (s *Server) ipBanCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/ready", "/metrics":
			next.ServeHTTP(w, r)
			return
		}
		if s.rdb != nil {
			if n, err := s.rdb.Exists(r.Context(), "global:ip-ban:"+clientIP(r)).Result(); err == nil && n > 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"code":"FORBIDDEN","message":"Acesso não autorizado."}`))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
