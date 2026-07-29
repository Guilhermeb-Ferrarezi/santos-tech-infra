package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/santos-tech/golog"
)

func main() {
	golog.InitLogging()
	cfg := LoadConfig()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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

	// Pool do domínio do portal. Se a URL for a mesma do auth, reusa o pool (um
	// só banco — o estado final desejado); senão abre um pool separado.
	portalDB := db
	if cfg.PortalDatabaseURL != "" && cfg.PortalDatabaseURL != cfg.DatabaseURL {
		portalDB, err = newDB(ctx, cfg.PortalDatabaseURL)
		if err != nil {
			slog.Error("falha ao conectar no Postgres do portal", "err", err)
			os.Exit(1)
		}
		defer portalDB.Close()
		slog.Info("portal usando banco separado do auth")
	}
	if err := migratePortal(ctx, portalDB); err != nil {
		slog.Error("falha na migração do portal", "err", err)
		os.Exit(1)
	}

	rdb, err := newRedis(cfg.RedisURL)
	if err != nil {
		slog.Error("falha ao conectar no Redis", "err", err)
		os.Exit(1)
	}
	defer rdb.Close()

	// Fila durável (asynq) sobre o MESMO Redis: o cliente enfileira os emails e um
	// asynq.Server embutido neste processo os consome com retry. ParseRedisURI usa
	// a mesma REDIS_URL (já validada acima ao abrir o rdb).
	asynqRedis, err := asynq.ParseRedisURI(cfg.RedisURL)
	if err != nil {
		slog.Error("REDIS_URL (asynq) inválida", "err", err)
		os.Exit(1)
	}
	queueClient := asynq.NewClient(asynqRedis)
	defer queueClient.Close()

	srv := NewServer(cfg, db, portalDB, rdb)
	srv.queue = queueClient
	srv.syncBansToRedis(ctx)

	// asynq.Server embutido: processa a fila de emails. Roda numa goroutine com
	// recover() e é parado no graceful shutdown (Stop deixa de puxar tasks novas,
	// Shutdown aguarda as em voo — sem perder o que já está no Redis).
	emailQueueSrv, emailQueueMux := srv.newEmailQueueServer(asynqRedis)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic na goroutine do asynq server", "panic", rec, "stack", string(debug.Stack()))
			}
		}()
		if err := emailQueueSrv.Run(emailQueueMux); err != nil {
			slog.Error("asynq server (emails) parou", "err", err)
			os.Exit(1)
		}
	}()

	// Limpeza periódica de sessões vencidas (sem isso a tabela cresce pra sempre).
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic no cleanup de sessões", "panic", rec, "stack", string(debug.Stack()))
			}
		}()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if n, err := srv.deleteExpiredSessions(ctx); err != nil {
				slog.Warn("cleanup de sessões falhou", "err", err)
			} else if n > 0 {
				slog.Info("sessões vencidas removidas", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	srv2 := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second, // anti slow-loris
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second, // folgado p/ respostas longas (ex.: status agregado)
		IdleTimeout:       30 * time.Second,
	}

	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic no servidor HTTP", "panic", rec, "stack", string(debug.Stack()))
			}
		}()
		slog.Info("auth ouvindo", "port", cfg.Port)
		if err := srv2.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("erro no servidor", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("encerrando: aguardando conexões em andamento")
	// Para de puxar tasks novas da fila antes de drenar o HTTP, para não aceitar
	// trabalho novo enquanto encerra.
	emailQueueSrv.Stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv2.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown não concluído no prazo", "err", err)
	}
	// Aguarda as tasks em voo terminarem (as não processadas continuam no Redis
	// para o próximo boot — nada se perde).
	emailQueueSrv.Shutdown()
}
