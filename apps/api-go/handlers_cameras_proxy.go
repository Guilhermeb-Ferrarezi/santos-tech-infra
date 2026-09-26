package main

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
)

func (s *Server) handleCamerasProxy(w http.ResponseWriter, r *http.Request) {
	if s.cfg.CamerasURL == "" {
		http.Error(w, `{"error":"cameras_service_not_configured"}`, http.StatusBadGateway)
		return
	}

	targetURL, err := url.Parse(s.cfg.CamerasURL)
	if err != nil {
		slog.Error("Invalid CAMERAS_SERVICE_URL", "url", s.cfg.CamerasURL, "error", err)
		http.Error(w, `{"error":"invalid_cameras_url"}`, http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = targetURL.Host
		// Encaminha IP real do cliente se disponível
		if clientIP := r.Header.Get("X-Forwarded-For"); clientIP != "" {
			req.Header.Set("X-Forwarded-For", clientIP)
		} else {
			req.Header.Set("X-Forwarded-For", r.RemoteAddr)
		}
	}

	proxy.ServeHTTP(w, r)
}
