package main

import (
	"net"
	"net/http"
	"strings"
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

// ipBanCheck bloqueia IPs banidos consultando Redis ("global:ip-ban:<ip>").
// Fail-open: Redis nil ou indisponível → passa. Endpoints operacionais são isentos.
func (s *Server) ipBanCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bp := s.cfg.BasePath
		p := r.URL.Path
		if p == bp+"/health" || p == bp+"/ready" || p == bp+"/metrics" {
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
