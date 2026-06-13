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

	// Semáforo global de processos Claude (teto de concorrência p/ não estourar RAM).
	initTurnSlots()

	// Limpeza de estado órfão: locks de turno e status 'running' que sobraram de um
	// deploy/restart anterior (o processo Claude morreu junto, mas a chave ficou).
	if err := cleanupOrphans(ctx, db, rdb); err != nil {
		// Não-fatal: é melhor subir e servir do que travar o boot por uma limpeza.
		slog.Warn("falha ao limpar estado órfão no boot", "err", err)
	}

	srv := NewServer(cfg, db, rdb)
	slog.Info("agent-go ouvindo", "port", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, srv.Routes()); err != nil {
		slog.Error("erro no servidor", "err", err)
		os.Exit(1)
	}
}
