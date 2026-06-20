package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/santos-tech/golog"
)

func main() {
	golog.InitLogging()
	cfg := LoadConfig()
	ctx := context.Background()

	// Subcomando operacional: registra o webhook na Efí e sai (sem subir o servidor).
	registerWebhook := flag.Bool("register-webhook", false, "registra o webhook na Efí e sai")
	flag.Parse()
	if *registerWebhook {
		if cfg.EFIWebhookURL == "" {
			slog.Error("EFI_WEBHOOK_URL ausente — defina a URL pública do webhook")
			os.Exit(1)
		}
		url := cfg.EFIWebhookURL
		if cfg.EFIWebhookSecret != "" {
			sep := "?"
			if strings.Contains(url, "?") {
				sep = "&"
			}
			url += sep + "token=" + cfg.EFIWebhookSecret
		}
		if err := newEfiProvider(cfg).RegisterWebhook(ctx, url); err != nil {
			slog.Error("falha ao registrar webhook na Efí", "err", err)
			os.Exit(1)
		}
		slog.Info("webhook registrado na Efí", "url", cfg.EFIWebhookURL)
		return
	}

	db, err := newDB(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("falha ao conectar no Postgres", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		slog.Error("falha na migração", "err", err)
		os.Exit(1)
	}

	if cfg.Production && cfg.EFIWebhookSecret == "" {
		// Não trava o boot (as cobranças ainda funcionam), mas o webhook fica fail-closed:
		// nenhum CHARGE_PAID será processado até configurar o secret.
		slog.Error("EFI_WEBHOOK_SECRET ausente em produção: webhooks serão RECUSADOS até configurar")
	}

	rdb, err := newRedis(cfg.RedisURL)
	if err != nil {
		slog.Error("falha ao conectar no Redis", "err", err)
		os.Exit(1)
	}
	defer rdb.Close()
	registerDBMetrics(db)
	provider := newEfiProvider(cfg)
	srv := NewServer(cfg, db, rdb, provider)

	// Fila durável (asynq) sobre o MESMO Redis. O client enfileira a notificação
	// de pagamento; o server embutido processa com retry/backoff.
	asynqRedis, err := asynq.ParseRedisURI(cfg.RedisURL)
	if err != nil {
		slog.Error("REDIS_URL (asynq) inválida", "err", err)
		os.Exit(1)
	}
	asynqClient := asynq.NewClient(asynqRedis)
	defer asynqClient.Close()
	srv.queue = asynqClient

	asynqSrv := asynq.NewServer(asynqRedis, asynq.Config{Concurrency: 5})
	mux := asynq.NewServeMux()
	mux.HandleFunc(TaskNotifyPaid, srv.HandleNotifyPaid)
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

	// Contexto cancelado em SIGINT/SIGTERM: derruba o loop de recorrência e
	// dispara o graceful shutdown do servidor HTTP.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	loopCtx, cancelLoop := context.WithCancel(ctx)
	defer cancelLoop()
	go srv.runRecurringLoop(loopCtx)

	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second, // anti slow-loris
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second, // generoso para o SSE de /pay/{token}/events
		IdleTimeout:       30 * time.Second,
	}

	go func() {
		slog.Info("payments ouvindo", "port", cfg.Port)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("erro no servidor", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("sinal recebido, encerrando graciosamente")

	// Para de puxar tasks novas antes de drenar o HTTP.
	asynqSrv.Stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("falha no graceful shutdown", "err", err)
	}

	// Aguarda as tasks em voo terminarem.
	asynqSrv.Shutdown()
}
