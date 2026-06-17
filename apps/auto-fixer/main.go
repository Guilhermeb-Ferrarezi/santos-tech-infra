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

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config inválida", "err", err)
		os.Exit(1)
	}

	ropt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		slog.Error("REDIS_URL inválida", "err", err)
		os.Exit(1)
	}
	rdb := redis.NewClient(ropt)

	asynqRedis, err := asynq.ParseRedisURI(cfg.RedisURL)
	if err != nil {
		slog.Error("REDIS_URL (asynq) inválida", "err", err)
		os.Exit(1)
	}

	defer func() { _ = rdb.Close() }()

	store := &Store{rdb: rdb}
	evo := NewEvolutionClient(cfg.EvolutionURL, cfg.EvolutionKey, cfg.EvolutionInst)
	cool := NewCoolifyClient(cfg.CoolifyAPIURL, cfg.CoolifyAPIToken)
	ghApp, err := NewGitHubApp(cfg.GHAppID, cfg.GHAppKey)
	if err != nil {
		slog.Error("github app inválido", "err", err)
		os.Exit(1)
	}
	if ghApp.Enabled() {
		slog.Info("github app habilitado (token efêmero escopado por repo)")
	} else {
		slog.Warn("github app não configurado — git via PAT (postura de segurança reduzida)")
	}
	client := asynq.NewClient(asynqRedis)
	defer client.Close()

	workers := &Workers{cfg: cfg, store: store, evo: evo, cool: cool, ghApp: ghApp, client: client}

	asynqSrv := asynq.NewServer(asynqRedis, asynq.Config{Concurrency: 5})
	mux := asynq.NewServeMux()
	mux.HandleFunc(TaskIncidentCreate, workers.HandleIncidentCreate)
	mux.HandleFunc(TaskFixRun, workers.HandleFixRun)
	mux.HandleFunc(TaskNotifyFinal, workers.HandleNotifyFinal)
	mux.HandleFunc(TaskDeployTimeout, workers.HandleDeployTimeout)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic na goroutine do asynq server", "panic", rec)
			}
		}()
		if err := asynqSrv.Run(mux); err != nil {
			slog.Error("asynq server parou", "err", err)
			os.Exit(1)
		}
	}()

	wh := &WebhookHandler{cfg: cfg, client: client, store: store, cool: cool}
	httpMux := http.NewServeMux()
	httpMux.Handle("POST /webhooks/coolify", wh)
	// /health, /ready e /metrics são públicos (sem auth) — senão o healthcheck quebra.
	httpMux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	httpMux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := rdb.Ping(ctx).Err(); err != nil {
			slog.Warn("readiness: redis indisponível", "err", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("redis unavailable"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	httpMux.Handle("GET /metrics", metricsHandler())

	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           requestLogger(metricsMiddleware(httpMux)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic na goroutine do http server", "panic", rec)
			}
		}()
		slog.Info("auto-fixer ouvindo", "port", cfg.Port)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server parou", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("encerrando…")
	// Para de puxar tasks novas, drena o HTTP e então aguarda as tasks em voo.
	asynqSrv.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	asynqSrv.Shutdown()
}
