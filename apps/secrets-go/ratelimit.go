package main

import (
	"net"
	"net/http"
	"strings"
)

// clientIP extrai o IP do cliente na mesma cadeia dos outros serviços Go do
// ecossistema: CF-Connecting-IP → X-Forwarded-For → RemoteAddr.
//
// CF-Connecting-IP vem primeiro porque a Cloudflare o REESCREVE a cada request
// (o cliente não consegue forjá-lo). O X-Forwarded-For, ao contrário, é
// ANEXADO: o Traefik acrescenta ao que o cliente mandou, então confiar no
// primeiro elemento era confiar num valor escolhido por quem chama — bastava
// mandar "X-Forwarded-For: 1.2.3.4" para escapar do ban global por IP. Ele
// segue como fallback (deploy sem Cloudflare na frente), mas só depois.
func clientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// ipBanCheck bloqueia IPs banidos consultando Redis ("global:ip-ban:<ip>") —
// o mesmo namespace usado pelos outros serviços Go do ecossistema. Fail-open:
// Redis indisponível OU REDIS_URL não configurada (s.rdb == nil) → passa.
// Endpoints operacionais são isentos.
//
// Nota: diferente de payments-go/bot-go/api-go, o secrets-go NÃO tem
// rate-limit por rota (regra 4 do CLAUDE.md) — toda rota de domínio exige
// admin autenticado (requireAdmin), então não há endpoint público sensível a
// abuso por volume. O ban global por IP é reaproveitado mesmo assim, por
// consistência de segurança (ex.: um IP banido por brute-force em outro
// serviço também não deve conseguir nem tentar autenticar aqui).
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
