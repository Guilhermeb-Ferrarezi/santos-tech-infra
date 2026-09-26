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

	"github.com/jackc/pgx/v5/pgxpool"
	"santos-tech.com/cameras-go/db"
)

type Server struct {
	cfg    Config
	db     *pgxpool.Pool
	q      *db.Queries
	go2rtc *Go2RTCClient
}

func NewServer(cfg Config) (*Server, error) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}

	return &Server{
		cfg:    cfg,
		db:     pool,
		q:      db.New(pool),
		go2rtc: NewGo2RTCClient(),
	}, nil
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	})

	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if err := s.db.Ping(r.Context()); err != nil {
			http.Error(w, `{"ok":false}`, http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	})

	// Auth middleware
	auth := s.authGuard

	// Câmeras
	mux.HandleFunc("GET /cameras", auth(s.handleListCameras))
	mux.HandleFunc("POST /cameras", s.requireAdmin(s.handleCreateCamera))
	mux.HandleFunc("GET /cameras/{id}", auth(s.handleGetCamera))
	mux.HandleFunc("PUT /cameras/{id}", s.requireAdmin(s.handleUpdateCamera))
	mux.HandleFunc("DELETE /cameras/{id}", s.requireAdmin(s.handleDeleteCamera))
	mux.HandleFunc("POST /cameras/{id}/test", s.requireAdmin(s.handleTestCamera))
	mux.HandleFunc("GET /cameras/{id}/stream", auth(s.handleProxyStream))

	// Gravações
	mux.HandleFunc("GET /cameras/{id}/recordings", auth(s.handleListRecordings))
	mux.HandleFunc("PATCH /cameras/recordings/{id}", s.requireAdmin(s.handlePatchRecording))
	mux.HandleFunc("DELETE /cameras/recordings/{id}", s.requireAdmin(s.handleDeleteRecording)) // deve ser sudo depois

	// Settings e OAuth (Admin)
	mux.HandleFunc("GET /cameras/settings", s.requireAdmin(s.handleGetSettings))
	mux.HandleFunc("PUT /cameras/settings", s.requireAdmin(s.handleUpdateSettings))
	mux.HandleFunc("GET /settings", s.requireAdmin(s.handleGetSettings))
	mux.HandleFunc("PUT /settings", s.requireAdmin(s.handleUpdateSettings))

	mux.HandleFunc("GET /cameras/oauth/url", s.requireAdmin(s.handleOAuthUrl))
	mux.HandleFunc("GET /oauth/url", s.requireAdmin(s.handleOAuthUrl))

	mux.HandleFunc("GET /cameras/oauth/callback", s.handleOAuthCallback)
	mux.HandleFunc("GET /auth/callback", s.handleOAuthCallback)
	mux.HandleFunc("GET /oauth/callback", s.handleOAuthCallback)

	return mux
}

func (s *Server) Run() error {
	srv := &http.Server{
		Addr:         ":" + s.cfg.Port,
		Handler:      s.Routes(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0, // 0 para permitir conexões WebSocket/streaming de longa duração
		IdleTimeout:  120 * time.Second,
	}

	idleConnsClosed := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt, syscall.SIGTERM)
		<-sigint

		slog.Info("Shutting down server...")

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("HTTP server Shutdown", "error", err)
		}
		s.db.Close()
		close(idleConnsClosed)
	}()

	slog.Info("Server listening on port " + s.cfg.Port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	<-idleConnsClosed
	slog.Info("Server stopped gracefully")
	return nil
}
