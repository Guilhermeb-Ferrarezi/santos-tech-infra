package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// trustedProxyNets é a allowlist de redes (CIDRs) de quem pode falar por nós via
// CF-Connecting-IP / X-Forwarded-For. Só confiamos nesses headers quando o
// RemoteAddr (o peer TCP real) cai aqui dentro. Caso contrário, qualquer cliente
// poderia forjar o IP a cada request e zerar o rate-limit (brute-force de
// senha/OTP). Configurável por TRUSTED_PROXY_CIDRS (CSV); senão usa um default
// com os ranges publicados da Cloudflare + redes privadas (onde o Traefik roda).
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
// consegue forjar o IP para escapar do rate-limit/lockout.
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

// allow incrementa o contador da chave e diz se ainda está no limite.
// failClosed: comportamento quando o Redis está indisponível. Para rotas
// SENSÍVEIS (login, forgot-password, mfa, oauth/token) usamos fail-CLOSED
// (bloqueia) para não virar fail-open de brute-force durante uma queda do Redis;
// para o limite global não-sensível mantemos fail-open (não derruba o site todo).
func (s *Server) allow(ctx context.Context, key string, max int, window time.Duration, failClosed bool) bool {
	n, err := s.rdb.Incr(ctx, key).Result()
	if err != nil {
		return !failClosed
	}
	// ExpireNX (Redis 7 EXPIRE … NX) sets the TTL only if the key has none yet —
	// single atomic command, no race between INCR and EXPIRE.
	s.rdb.ExpireNX(ctx, key, window)
	return n <= int64(max)
}

// rateLimit limita por (rota + IP) — usar nas rotas sensíveis. Fail-CLOSED no
// erro do Redis: estas rotas são alvo de brute-force, então preferimos negar a
// abrir. (O Redis também é dependência do login em si, então uma queda já
// degrada o serviço de qualquer forma.)
func (s *Server) rateLimit(max int, window time.Duration, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := fmt.Sprintf("rl:%s:%s", r.URL.Path, clientIP(r))
		if !s.allow(r.Context(), key, max, window, true) {
			writeErr(w, appErr(http.StatusTooManyRequests, "RATE_LIMITED", "Muitas tentativas. Tente novamente em instantes."))
			return
		}
		next(w, r)
	}
}

// globalRateLimit limita o total de requisições por IP (proteção geral).
// Fail-OPEN: como cobre TODAS as rotas (inclusive as públicas e não-sensíveis),
// uma queda do Redis não deve derrubar o site inteiro; as rotas sensíveis têm o
// seu próprio rateLimit fail-closed por cima.
func (s *Server) globalRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allow(r.Context(), "rl:global:"+clientIP(r), 200, time.Minute, false) {
			writeErr(w, appErr(http.StatusTooManyRequests, "RATE_LIMITED", "Muitas requisições. Aguarde um momento."))
			return
		}
		next.ServeHTTP(w, r)
	})
}
