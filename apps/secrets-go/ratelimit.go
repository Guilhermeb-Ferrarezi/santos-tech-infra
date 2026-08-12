package main

import (
	"net/http"
	"strings"
)

// clientIP extrai o IP do cliente (mesmo esquema de payments-go/handlers_links.go):
// X-Forwarded-For (atrás de Cloudflare + Traefik) → RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if idx := strings.LastIndexByte(r.RemoteAddr, ':'); idx != -1 {
		return r.RemoteAddr[:idx]
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
