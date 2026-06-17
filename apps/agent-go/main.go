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

	"github.com/santos-tech/golog"
)

func main() {
	golog.InitLogging()
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

	// Fecha as conexões com Postgres e Redis ao sair (após o Shutdown drenar o HTTP).
	defer db.Close()
	defer func() {
		if err := rdb.Close(); err != nil {
			slog.Warn("erro ao fechar Redis", "err", err)
		}
	}()

	srv := NewServer(cfg, db, rdb)
	httpSrv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: srv.Routes(),
		// ReadHeaderTimeout/ReadTimeout curtos mitigam slow-loris. WriteTimeout
		// precisa cobrir o SSE de /generate/stream (timeout interno de 120s do
		// claude) — fica acima disso p/ não cortar o stream no meio. O WebSocket
		// usa Hijack, então o WriteTimeout não se aplica após o upgrade.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      150 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	// Encerra o servidor ao receber SIGINT/SIGTERM (deploy/restart).
	shutCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("agent-go ouvindo", "port", cfg.Port)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("erro no servidor", "err", err)
			os.Exit(1)
		}
	}()

	<-shutCtx.Done()
	slog.Info("encerrando agent-go (graceful shutdown)")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("shutdown forçado", "err", err)
	}
}
