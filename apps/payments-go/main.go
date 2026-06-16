package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
)

func main() {
	cfg := LoadConfig()
	ctx := context.Background()

	db, err := newDB(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("falha ao conectar no Postgres", "err", err)
		os.Exit(1)
	}
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
	provider := newDotfyProvider(cfg)
	srv := NewServer(cfg, db, rdb, provider)

	go srv.runRecurringLoop(ctx)

	slog.Info("payments ouvindo", "port", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, srv.Routes()); err != nil {
		slog.Error("erro no servidor", "err", err)
		os.Exit(1)
	}
}
