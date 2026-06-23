package main

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
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
