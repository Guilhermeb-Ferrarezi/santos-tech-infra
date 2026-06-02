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

	if err := os.MkdirAll(cfg.WorkspaceRoot, 0o755); err != nil {
		slog.Error("falha ao criar workspace root", "err", err, "dir", cfg.WorkspaceRoot)
		os.Exit(1)
	}

	db, err := newDB(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("falha ao conectar no Postgres", "err", err)
		os.Exit(1)
	}
	if err := migrate(ctx, db); err != nil {
		slog.Error("falha na migração", "err", err)
		os.Exit(1)
	}

	rdb, err := newRedis(cfg.RedisURL)
	if err != nil {
		slog.Error("falha ao conectar no Redis", "err", err)
		os.Exit(1)
	}

	srv := NewServer(cfg, db, rdb)
	slog.Info("agent-go ouvindo", "port", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, srv.Routes()); err != nil {
		slog.Error("erro no servidor", "err", err)
		os.Exit(1)
	}
}
