package main

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Coletores Prometheus do bot. promauto registra no DefaultRegisterer uma única
// vez (variáveis de pacote) — registrar duas vezes causaria panic no boot.
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

	// bgPoolRejected conta tarefas de webhook descartadas por falta de slot no
	// pool de background — sinal de que BG_POOL_SLOTS está apertado demais.
	bgPoolRejected = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bot_bg_pool_rejected_total",
			Help: "Tarefas de background descartadas por falta de slot no pool.",
		},
		[]string{"task"},
	)
)

// metricsMiddleware mede cada requisição e alimenta os coletores. Usa o padrão de
// rota casado pelo ServeMux (r.Pattern) como label "path" para manter a
// cardinalidade baixa — IDs na URL não viram séries distintas.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mw := &metricsResponseWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(mw, r)

		// r.Pattern é preenchido pelo ServeMux ao casar a rota (Go 1.22+).
		// Sem casamento (404), cai para o path cru — mas limitamos a "<unmatched>"
		// para não explodir a cardinalidade com URLs arbitrárias.
		path := r.Pattern
		if path == "" {
			path = "<unmatched>"
		}

		httpRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(mw.status)).Inc()
		httpRequestDuration.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
	})
}

// metricsResponseWriter captura o status para o label da métrica, delegando
// Flush/Hijack quando o writer subjacente os suportar (SSE/WebSocket).
type metricsResponseWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *metricsResponseWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *metricsResponseWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func (w *metricsResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *metricsResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("metrics: underlying ResponseWriter does not support Hijack")
}
