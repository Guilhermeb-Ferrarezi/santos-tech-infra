package main

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Coletores registrados UMA única vez via promauto (registrar 2x daria panic no boot).
var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total de requisições HTTP por método, rota (padrão do mux) e status.",
	}, []string{"method", "path", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Latência das requisições HTTP em segundos.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
)

// metricsResponseWriter captura o status para a métrica. Implementa Flush/Hijack
// via interfaces opcionais checadas em runtime (preserva SSE/WebSocket).
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
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

// metricsMiddleware conta requisições e mede latência. Usa o padrão de rota casado
// pelo ServeMux (r.Pattern, Go 1.22+) em vez do path bruto, evitando explosão de
// cardinalidade com IDs na URL. Deve ser plugado DENTRO do mux (após o roteamento).
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mw := &metricsResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(mw, r)

		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		httpRequestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(mw.status)).Inc()
		httpRequestDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

// registerPoolMetrics expõe gauges do pool pgx do auth (conexões totais/ociosas/em uso).
// Registra via Func collectors (lidos no scrape). Idempotente para o caso de pool nil.
func registerPoolMetrics(pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "pgx_pool_total_conns",
		Help: "Conexões totais no pool pgx (em uso + ociosas).",
	}, func() float64 { return float64(pool.Stat().TotalConns()) })
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "pgx_pool_idle_conns",
		Help: "Conexões ociosas no pool pgx.",
	}, func() float64 { return float64(pool.Stat().IdleConns()) })
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "pgx_pool_acquired_conns",
		Help: "Conexões em uso no pool pgx.",
	}, func() float64 { return float64(pool.Stat().AcquiredConns()) })
}

// metricsHandler serve o /metrics no formato Prometheus.
//
// Quando METRICS_TOKEN esta definida, exige "Authorization: Bearer <token>" no
// scrape. Sem isso o /metrics publica para qualquer um na internet o mapa
// completo das rotas (inclusive as administrativas), o volume por rota/status e
// o estado do pool do Postgres. Com a env vazia o endpoint segue aberto, para
// nao derrubar o scrape existente antes de o token ser configurado no coletor.
func metricsHandler() http.Handler {
	inner := promhttp.Handler()
	token := os.Getenv("METRICS_TOKEN")
	if token == "" {
		return inner
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ConstantTimeCompare para nao vazar o token por timing.
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	})
}
