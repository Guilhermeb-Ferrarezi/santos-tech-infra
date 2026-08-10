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
	golog.InitSentry("payments-go")
	defer golog.FlushSentry()
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
		prov := newEfiProvider(cfg)
		// Webhook pix (débito imediato e ciclos de recorrência).
		pixURL := cfg.EFIWebhookURL
		if cfg.EFIWebhookSecret != "" {
			sep := "?"
			if strings.Contains(pixURL, "?") {
				sep = "&"
			}
			pixURL += sep + "token=" + cfg.EFIWebhookSecret
		}
		if err := prov.RegisterWebhook(ctx, pixURL); err != nil {
			slog.Error("falha ao registrar webhook pix na Efí", "err", err)
			os.Exit(1)
		}
		slog.Info("webhook pix registrado na Efí", "url", cfg.EFIWebhookURL)
		// Webhook de recorrências (PIX Automático): rota /rec, mesmo segredo em ?token=.
		// Registrado AQUI (comando), não no boot: a Efí faz um POST de validação esperando
		// 200, e no boot o servidor HTTP ainda não está ouvindo → Traefik 502.
		recURL := cfg.EFIWebhookURL + "/rec"
		if cfg.EFIWebhookSecret != "" {
			recURL += "?token=" + cfg.EFIWebhookSecret
		}
		if err := prov.RegisterRecWebhook(ctx, recURL); err != nil {
			slog.Error("falha ao registrar webhook rec na Efí", "err", err)
			os.Exit(1)
		}
		slog.Info("webhook rec registrado na Efí", "url", cfg.EFIWebhookURL+"/rec")
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
	go srv.runExpiryLoop(loopCtx)
	go srv.runRecurrenceCycleLoop(loopCtx)
	// O webhook de recorrências NÃO é registrado no boot (a validação da Efí pegaria o
	// servidor antes de ouvir → 502). Registre via `payments -register-webhook` após o deploy,
	// junto do webhook pix (mesmo padrão).

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
