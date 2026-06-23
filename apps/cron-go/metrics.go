package main

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Métricas específicas de cron (registradas no init via promauto).
var (
	cronRunsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "cron_go_runs_total",
		Help: "Total de execuções de jobs por status (success/failed/skipped_overlap).",
	}, []string{"status"})
	cronRunDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "cron_go_run_duration_seconds",
		Help:    "Duração de uma execução de job (início ao fim, incluindo retries).",
		Buckets: prometheus.DefBuckets,
	})
)

func registerDBMetrics(pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "cron_go_db_pool_total_conns",
		Help: "Total de conexões no pool do Postgres.",
	}, func() float64 { return float64(pool.Stat().TotalConns()) })
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "cron_go_db_pool_acquired_conns",
		Help: "Conexões atualmente em uso no pool do Postgres.",
	}, func() float64 { return float64(pool.Stat().AcquiredConns()) })
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "cron_go_db_pool_idle_conns",
		Help: "Conexões ociosas no pool do Postgres.",
	}, func() float64 { return float64(pool.Stat().IdleConns()) })
}
