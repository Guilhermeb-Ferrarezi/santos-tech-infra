package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
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
	notionClient := NewNotionClient(cfg.NotionToken, cfg.NotionAgendaDBID)

	// 8. Instancia AgentGoClient (Responder)
	sitemapCache := NewSitemapCache(cfg.SiteURL)
	agentClient := NewAgentGoClient(cfg.AgentGoURL, cfg.AgentGoSecret, sitemapCache, notionClient)

	// 9. Instancia WhatsAppSender + cliente Evolution (canal não-oficial)
	sender := NewWhatsAppSender(cfg.MetaAccessToken, cfg.MetaPhoneNumberID)
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
	worker := NewWorker(WorkerDeps{
		Config:            cfg,
		DB:                pool,
		Pool:              pool,
		Redis:             redisClient,
		Outbox:            outbox,
		ScheduledContacts: scheduled,
		Webhook:           webhooks,
		Engine:            engine,
		EvoEngine:         evoEngine,
		Sender:            sender,
		EvolutionSender:   evolutionClient,
		Logger:            logger,
		AgentGo:           agentClient,
	})

	// 13. Instancia Server
	server := NewServer(cfg, engine, webhooks, pool, sender, logger, hub, logRepo, evoEngine, evolutionClient)

	// 14. Inicia worker em background
	go worker.Start(ctx)

	// 15. Configura servidor HTTP
	httpServer := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: server.Handler(),
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

	_ = leads
	_ = scheduled
}
