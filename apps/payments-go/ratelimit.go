package main

import (
	"net/http"
)

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
