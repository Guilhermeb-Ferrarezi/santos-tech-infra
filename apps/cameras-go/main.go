package main

import (
	"context"
	"log/slog"
	"os"
)

func main() {
	// Inicialização básica do slog estruturado
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg := LoadConfig()

	srv, err := NewServer(cfg)
	if err != nil {
		slog.Error("Failed to initialize server", "error", err)
		os.Exit(1)
	}

	recorder := NewRecorderService(srv.q, srv.go2rtc, srv.cfg)
	recorder.Start(context.Background())

	if err := srv.Run(); err != nil {
		slog.Error("Server error", "error", err)
		os.Exit(1)
	}
}
