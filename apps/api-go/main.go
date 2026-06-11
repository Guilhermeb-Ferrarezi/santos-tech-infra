package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"
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

	// Pool do domínio do portal. Se a URL for a mesma do auth, reusa o pool (um
	// só banco — o estado final desejado); senão abre um pool separado.
	portalDB := db
	if cfg.PortalDatabaseURL != "" && cfg.PortalDatabaseURL != cfg.DatabaseURL {
		portalDB, err = newDB(ctx, cfg.PortalDatabaseURL)
		if err != nil {
			slog.Error("falha ao conectar no Postgres do portal", "err", err)
			os.Exit(1)
		}
		slog.Info("portal usando banco separado do auth")
	}

	rdb, err := newRedis(cfg.RedisURL)
	if err != nil {
		slog.Error("falha ao conectar no Redis", "err", err)
		os.Exit(1)
	}

	srv := NewServer(cfg, db, portalDB, rdb)

	// Limpeza periódica de sessões vencidas (sem isso a tabela cresce pra sempre).
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if n, err := srv.deleteExpiredSessions(ctx); err != nil {
				slog.Warn("cleanup de sessões falhou", "err", err)
			} else if n > 0 {
				slog.Info("sessões vencidas removidas", "count", n)
			}
			<-t.C
		}
	}()

	slog.Info("auth ouvindo", "port", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, srv.Routes()); err != nil {
		slog.Error("erro no servidor", "err", err)
		os.Exit(1)
	}
}
