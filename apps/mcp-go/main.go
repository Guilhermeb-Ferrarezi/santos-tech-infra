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

	openapi, err := os.ReadFile(cfg.OpenAPIPath)
	if err != nil {
		slog.Warn("openapi.yaml não encontrado — resource santos-tech://openapi/auth desabilitado",
			"path", cfg.OpenAPIPath, "err", err)
	}

	srv := NewServer(cfg, openapi)

	// WriteTimeout folgado (60s) cobre respostas SSE do Streamable HTTP sem deixar
	// conexão pendurada para sempre; ReadHeaderTimeout/IdleTimeout protegem do resto.
	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	// Encerra ao receber SIGINT/SIGTERM, drenando requisições em voo.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic no servidor http", "panic", r)
				errc <- http.ErrServerClosed
			}
		}()
		slog.Info("mcp ouvindo", "port", cfg.Port)
		errc <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("erro no servidor", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		slog.Info("sinal recebido, encerrando")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			slog.Error("erro no graceful shutdown", "err", err)
		}
	}
}
