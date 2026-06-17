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
)

func main() {
	initLogging()
	cfg := LoadConfig()
	ctx := context.Background()

	db, err := newDB(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("falha ao conectar no Postgres", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		slog.Error("falha na migração", "err", err)
		os.Exit(1)
	}

	if cfg.Production && cfg.DotfyWebhookSecret == "" {
		// Não trava o boot (as cobranças ainda funcionam), mas o webhook fica fail-closed:
		// nenhum CHARGE_PAID/EXPIRED será processado até configurar o secret HMAC.
		slog.Error("DOTFY_WEBHOOK_SECRET ausente em produção: webhooks serão RECUSADOS até configurar")
	}

	rdb, err := newRedis(cfg.RedisURL)
	if err != nil {
		slog.Error("falha ao conectar no Redis", "err", err)
		os.Exit(1)
	}
	defer rdb.Close()
	registerDBMetrics(db)
	provider := newDotfyProvider(cfg)
	srv := NewServer(cfg, db, rdb, provider)

	// Contexto cancelado em SIGINT/SIGTERM: derruba o loop de recorrência e
	// dispara o graceful shutdown do servidor HTTP.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	loopCtx, cancelLoop := context.WithCancel(ctx)
	defer cancelLoop()
	go srv.runRecurringLoop(loopCtx)

	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second, // anti slow-loris
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second, // generoso para o SSE de /pay/{token}/events
		IdleTimeout:       30 * time.Second,
	}

	go func() {
		slog.Info("payments ouvindo", "port", cfg.Port)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("erro no servidor", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("sinal recebido, encerrando graciosamente")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("falha no graceful shutdown", "err", err)
	}
}
