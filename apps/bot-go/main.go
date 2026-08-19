package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/santos-tech/golog"
)

func main() {
	// 1. Carrega configuração
	cfg := LoadConfig()

	// 2. Configura slog (JSON em produção, texto em desenvolvimento)
	var handler slog.Handler
	if cfg.Production {
		handler = slog.NewJSONHandler(os.Stdout, nil)
	} else {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	}
	logger := slog.New(handler)
	slog.SetDefault(logger)

	golog.InitSentry("bot-go")
	defer golog.FlushSentry()

	// LGPD: com LOG_BODIES ligado, o corpo dos webhooks da Meta/Evolution e das
	// rotas de conversa vai íntegro para o Loki (text.body, from, wa_id,
	// profile.name não são redigidos pelo golog). O default do serviço é 0
	// (Dockerfile); se alguém religar, que seja com aviso no log.
	warnIfBodyLoggingOn(logger)

	// 3. Context cancelável via sinal do SO
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 4. Conecta ao Postgres
	logger.Info("conectando ao Postgres...")
	pool, err := openDB(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("falha ao conectar ao Postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	logger.Info("Postgres conectado")

	// 5. Roda migrações
	logger.Info("rodando migrações...")
	if err := runMigrations(ctx, pool); err != nil {
		logger.Error("falha nas migrações", "err", err)
		os.Exit(1)
	}
	logger.Info("migrações concluídas")

	// 6. Conecta ao Redis
	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		logger.Error("URL do Redis inválida", "err", err)
		os.Exit(1)
	}
	redisClient := redis.NewClient(redisOpts)
	defer func() { _ = redisClient.Close() }()

	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.Error("falha ao conectar ao Redis", "err", err)
		os.Exit(1)
	}
	logger.Info("Redis conectado")

	// 7. Instancia repos
	contacts := NewContactRepo(pool)
	convs := NewConversationRepo(pool)
	messages := NewMessageRepo(pool)
	leads := NewLeadRepo(pool)
	outbox := NewOutboxRepo(pool)
	webhooks := NewWebhookRepo(pool)
	tenantCfg := NewTenantConfigRepo(pool)
	scheduled := NewScheduledContactRepo(pool)
	logRepo := NewProcessingLogRepo(pool)
	pending := NewPendingQuestionRepo(pool)
	bookings := NewPendingBookingRepo(pool)
	notionClient := NewNotionClient(cfg.NotionToken, cfg.NotionExperimentalDSID)

	// 8. Instancia AgentGoClient (Responder)
	sitemapCache := NewSitemapCache(cfg.SiteURL)
	agentClient := NewAgentGoClient(cfg.AgentGoURL, cfg.AgentGoSecret, sitemapCache, notionClient)

	// 9. Instancia WhatsAppSender + cliente Evolution (canal não-oficial)
	sender := NewWhatsAppSender(cfg.MetaAccessToken, cfg.MetaPhoneNumberID)
	voiceClient := NewVoiceClient(cfg)
	evolutionClient := NewEvolutionClient(cfg.EvolutionAPIURL, cfg.EvolutionAPIKey, cfg.EvolutionInstance)

	// 10. Instancia WSHub e inicia loop em background
	hub := NewWSHub(logger, cfg.DashCORSOrigin)
	go hub.Run(ctx)

	// 11. Instancia ConversationEngine
	engine := NewConversationEngine(EngineDeps{
		TenantID:        cfg.TenantID,
		DB:              pool,
		Contacts:        contacts,
		Convs:           convs,
		Messages:        messages,
		Leads:           leads,
		Config:          tenantCfg,
		Responder:       agentClient,
		Sender:          sender,
		EvolutionSender: evolutionClient,
		Emitter:         outbox,
		Logger:          logger,
		Broadcast:       hub.Broadcast,
		LogRepo:         logRepo,
		TenantCfgRepo:   tenantCfg,
		Pending:         pending,
		Bookings:        bookings,
		Notion:          notionClient,
		Voice:           voiceClient,
	})

	// 11b. Engine para o canal Evolution: mesmos repos, mas responde via Evolution.
	// ForceBotEnabled — o gate é o toggle (evolution_bot_reply_enabled), não o whitelist.
	evoEngine := NewConversationEngine(EngineDeps{
		TenantID:        cfg.TenantID,
		DB:              pool,
		Contacts:        contacts,
		Convs:           convs,
		Messages:        messages,
		Leads:           leads,
		Config:          tenantCfg,
		Responder:       agentClient,
		Sender:          evolutionClient,
		EvolutionSender: evolutionClient,
		Emitter:         outbox,
		Logger:          logger,
		Broadcast:       hub.Broadcast,
		LogRepo:         logRepo,
		TenantCfgRepo:   tenantCfg,
		Pending:         pending,
		Bookings:        bookings,
		Notion:          notionClient,
		ForceBotEnabled: true,
	})

	// 12. Instancia Worker
	// O consumidor do Redis Stream compartilha key/group com o produtor do
	// Server (ambos derivados de cfg) — o consumer name é único por instância.
	retryStream := NewRetryStream(redisClient, cfg.RetryStreamKey, cfg.RetryStreamGroup, cfg.RetryStreamConsumer, logger)
	worker := NewWorker(WorkerDeps{
		Config:            cfg,
		DB:                pool,
		Pool:              pool,
		Redis:             redisClient,
		Outbox:            outbox,
		ScheduledContacts: scheduled,
		Webhook:           webhooks,
		RetryStream:       retryStream,
		Engine:            engine,
		EvoEngine:         evoEngine,
		Sender:            sender,
		EvolutionSender:   evolutionClient,
		Logger:            logger,
		AgentGo:           agentClient,
	})

	// 13. Instancia Server
	server := NewServer(cfg, engine, webhooks, pool, sender, logger, hub, logRepo, evoEngine, evolutionClient, voiceClient, redisClient)

	// 14. Inicia worker em background; workerDone fecha quando ele drena no shutdown.
	workerDone := make(chan struct{})
	go func() {
		worker.Start(ctx)
		close(workerDone)
	}()

	// 14b. Poller do Sentry (avisa por WhatsApp issue nova) — no-op sem SENTRY_API_TOKEN.
	go server.startSentryPoller(ctx)

	// 15. Configura servidor HTTP
	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	// 16. Inicia servidor HTTP em goroutine separada
	go func() {
		logger.Info("servidor HTTP iniciado", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("servidor HTTP encerrou com erro", "err", err)
			stop()
		}
	}()

	// 17. Aguarda sinal de shutdown
	<-ctx.Done()
	logger.Info("sinal de encerramento recebido, iniciando graceful shutdown...")

	// 18. Graceful shutdown com timeout de 10s
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("erro no graceful shutdown", "err", err)
	} else {
		logger.Info("servidor HTTP encerrado com sucesso")
	}

	// Aguarda o worker (consumer do Redis Stream + loops) drenar antes que os
	// defers fechem Postgres/Redis — evita uso-após-fechar no XACK/queries.
	select {
	case <-workerDone:
		logger.Info("worker encerrado com sucesso")
	case <-time.After(10 * time.Second):
		logger.Warn("timeout aguardando worker encerrar")
	}

	_ = leads
	_ = scheduled
}

// warnIfBodyLoggingOn avisa quando LOG_BODIES está explicitamente ligado. O
// golog captura request+response e redige por NOME de chave; os campos do
// payload da Meta (text.body, from, wa_id, profile.name) não casam com nenhuma
// regra e seriam logados em claro, em nível INFO, a cada mensagem recebida.
//
// A flag do golog é lida no init do pacote — antes do main —, então não dá para
// forçá-la aqui; o default seguro vem do ENV LOG_BODIES=0 no Dockerfile.
func warnIfBodyLoggingOn(logger *slog.Logger) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_BODIES"))) {
	case "1", "true", "yes", "on":
		logger.Warn("LOG_BODIES ligado: corpo de webhooks e conversas irá em claro para o log (dados pessoais). Desligue em produção.")
	}
}
