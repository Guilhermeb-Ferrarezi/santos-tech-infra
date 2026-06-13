package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// clientIP extrai o IP real (atrás de Cloudflare + Traefik).
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// allow incrementa o contador da chave e diz se ainda está no limite.
// Fail-open: se o Redis cair, não bloqueia.
func (s *Server) allow(ctx context.Context, key string, max int, window time.Duration) bool {
	n, err := s.rdb.Incr(ctx, key).Result()
	if err != nil {
		return true
	}
	// ExpireNX (Redis 7 EXPIRE … NX) sets the TTL only if the key has none yet —
	// single atomic command, no race between INCR and EXPIRE.
	s.rdb.ExpireNX(ctx, key, window)
	return n <= int64(max)
}

// rateLimit limita por (rota + IP) — usar nas rotas sensíveis.
func (s *Server) rateLimit(max int, window time.Duration, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := fmt.Sprintf("rl:%s:%s", r.URL.Path, clientIP(r))
		if !s.allow(r.Context(), key, max, window) {
			writeErr(w, appErr(http.StatusTooManyRequests, "RATE_LIMITED", "Muitas tentativas. Tente novamente em instantes."))
			return
		}
		next(w, r)
	}
}

// globalRateLimit limita o total de requisições por IP (proteção geral).
func (s *Server) globalRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allow(r.Context(), "rl:global:"+clientIP(r), 200, time.Minute) {
			writeErr(w, appErr(http.StatusTooManyRequests, "RATE_LIMITED", "Muitas requisições. Aguarde um momento."))
			return
		}
		next.ServeHTTP(w, r)
	})
}
