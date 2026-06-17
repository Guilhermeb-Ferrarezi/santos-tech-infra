package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	initLogging()
	cfg := LoadConfig()

	openapi, err := os.ReadFile(cfg.OpenAPIPath)
	if err != nil {
		slog.Warn("openapi.yaml não encontrado — resource santos-tech://openapi/auth desabilitado",
			"path", cfg.OpenAPIPath, "err", err)
	}

	srv := NewServer(cfg, openapi)

	// Sem WriteTimeout: respostas SSE do Streamable HTTP podem ficar abertas.
	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	slog.Info("mcp ouvindo", "port", cfg.Port)
	if err := httpSrv.ListenAndServe(); err != nil {
		slog.Error("erro no servidor", "err", err)
		os.Exit(1)
	}
}
