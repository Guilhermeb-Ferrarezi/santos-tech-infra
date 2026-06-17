package main

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Métricas Prometheus do serviço. promauto registra os coletores no registry
// default UMA vez (registrar 2x causa panic no boot) — por isso são vars de
// pacote inicializadas no load.
var (
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total de requisições HTTP por método, rota e status.",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Duração das requisições HTTP em segundos.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)
)

// metricsHandler expõe o /metrics no formato do Prometheus.
func metricsHandler() http.Handler {
	return promhttp.Handler()
}

// metricsResponseWriter captura o status para a métrica, preservando Flush/Hijack
// (SSE/WebSocket) do writer original.
type metricsResponseWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *metricsResponseWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.status = code
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *metricsResponseWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *metricsResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *metricsResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// metricsMiddleware conta requisições e mede a duração. Usa o padrão de rota
// casado (r.Pattern) como label `path` p/ evitar alta cardinalidade com IDs em
// URLs (ex: /claude/conversations/{id}); cai para o path cru se não houver padrão.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mw := &metricsResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(mw, r)

		path := metricsRoute(r)
		httpRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(mw.status)).Inc()
		httpRequestDuration.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
	})
}

// metricsRoute devolve o padrão de rota casado pelo mux (sem o verbo HTTP), ou o
// path cru como fallback. Mantém a cardinalidade baixa no Prometheus.
func metricsRoute(r *http.Request) string {
	if p := r.Pattern; p != "" {
		// r.Pattern vem como "POST /claude/conversations/{id}"; tira o método.
		if i := strings.IndexByte(p, '/'); i >= 0 {
			return p[i:]
		}
		return p
	}
	return r.URL.Path
}
