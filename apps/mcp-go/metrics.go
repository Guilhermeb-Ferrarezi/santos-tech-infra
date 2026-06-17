package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Coletores registrados UMA vez no default registry (promauto). Registrar duas
// vezes causaria panic no boot — por isso são vars de pacote.
var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total de requisições HTTP por método, rota e status.",
	}, []string{"method", "path", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Duração das requisições HTTP em segundos.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
)

// metricsMiddleware conta requisições e mede latência. A rota é normalizada para
// o padrão registrado no mux (evita explosão de cardinalidade em /metrics).
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &metricsRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		path := metricsPath(r.URL.Path)
		httpRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(rec.status)).Inc()
		httpRequestDuration.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
	})
}

// metricsPath colapsa caminhos do MCP num rótulo estável. Endpoints conhecidos
// mantêm o próprio nome; qualquer outra coisa cai em "/" (a raiz é o MCP).
func metricsPath(p string) string {
	switch p {
	case "/health", "/mcp/health", "/ready", "/mcp/ready", "/metrics",
		"/.well-known/oauth-protected-resource", "/mcp/.well-known/oauth-protected-resource":
		return p
	default:
		return "/"
	}
}

type metricsRecorder struct {
	http.ResponseWriter
	status int
}

func (m *metricsRecorder) WriteHeader(code int) {
	m.status = code
	m.ResponseWriter.WriteHeader(code)
}

// Flush e Hijack delegados para preservar SSE/WebSocket do Streamable HTTP.
func (m *metricsRecorder) Flush() {
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (m *metricsRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := m.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("ResponseWriter não suporta Hijack")
}
