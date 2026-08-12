package main

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// promauto registra os coletores no DefaultRegisterer no init (uma única vez).
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

// metricsHandler expõe os coletores Prometheus em /metrics.
func metricsHandler() http.Handler {
	return promhttp.Handler()
}

// registerDBMetrics registra gauges do pool do Postgres lidos a cada scrape.
// Deve ser chamado UMA vez (no boot) — registrar 2x causa panic.
func registerDBMetrics(pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "pgxpool_total_conns",
		Help: "Total de conexões no pool do Postgres.",
	}, func() float64 { return float64(pool.Stat().TotalConns()) })
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "pgxpool_acquired_conns",
		Help: "Conexões atualmente em uso no pool do Postgres.",
	}, func() float64 { return float64(pool.Stat().AcquiredConns()) })
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "pgxpool_idle_conns",
		Help: "Conexões ociosas no pool do Postgres.",
	}, func() float64 { return float64(pool.Stat().IdleConns()) })
}

// metricsMiddleware conta requisições e mede latência. Usa o padrão da rota
// (r.Pattern, ex.: "GET /api/repos") como label para limitar a cardinalidade;
// cai para o path cru se o pattern não estiver disponível.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mw := &metricsWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(mw, r)

		path := r.Pattern
		if path == "" {
			path = r.URL.Path
		}
		httpRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(mw.status)).Inc()
		httpRequestDuration.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
	})
}

// metricsWriter captura o status code escrito pelo handler.
type metricsWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *metricsWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *metricsWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush e Hijack são delegados para preservar o SSE de /api/events.
func (w *metricsWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *metricsWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}
