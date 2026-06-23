package main

import (
	"net"
	"net/http"
	"strings"

	"github.com/redis/go-redis/v9"
)

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

// ipBanMiddleware bloqueia IPs banidos consultando Redis ("global:ip-ban:<ip>").
// Fail-open: Redis nil ou indisponível → passa. Endpoints operacionais são isentos.
func ipBanMiddleware(rdb *redis.Client, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/ready", "/metrics":
			next.ServeHTTP(w, r)
			return
		}
		if rdb != nil {
			if n, err := rdb.Exists(r.Context(), "global:ip-ban:"+clientIP(r)).Result(); err == nil && n > 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"code":"FORBIDDEN","message":"Acesso não autorizado."}`))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
