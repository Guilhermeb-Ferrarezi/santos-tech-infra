package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/santos-tech/golog"
)

func main() {
	golog.InitLogging()
	golog.InitSentry("cron-go")
	defer golog.FlushSentry()
	cfg := LoadConfig()
	ctx := context.Background()

	pool, err := newDB(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("falha ao conectar no Postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := migrate(ctx, pool); err != nil {
		slog.Error("falha na migração", "err", err)
		os.Exit(1)
	}
	registerDBMetrics(pool)

	var rdb *redis.Client
	if cfg.RedisURL != "" {
		ropt, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			slog.Warn("REDIS_URL inválida — ban check desabilitado", "err", err)
		} else {
			rdb = redis.NewClient(ropt)
			defer rdb.Close()
		}
	}

	srv := NewServer(cfg, pool, rdb)
	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	schedCtx, cancelSched := context.WithCancel(rootCtx)
	defer cancelSched()
	go srv.RunScheduler(schedCtx)

	go func() {
		slog.Info("cron-go ouvindo", "port", cfg.Port)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("servidor HTTP parou", "err", err)
			os.Exit(1)
		}
	}()

	<-rootCtx.Done()
	slog.Info("shutdown iniciado")
	// schedCtx já foi cancelado (deriva de rootCtx): o scheduler para de
	// reivindicar novos jobs. Drena o HTTP e os dispatches em voo.
	shutCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		slog.Error("shutdown com erro", "err", err)
	}
	slog.Info("drenando dispatches em voo")
	srv.WaitDrain(15 * time.Second)
	slog.Info("shutdown concluído")
}
