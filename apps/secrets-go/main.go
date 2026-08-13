// Command secrets-go escaneia o GitHub em busca de secrets vazados
// publicamente (chaves de API, tokens etc.), indexa os achados no
// Elasticsearch e faz verificação ativa (read-only) contra os provedores
// conhecidos para confirmar se a chave ainda está viva. É a portabilidade do
// scanner que rodava standalone em dotfy-scanner/backend para dentro do
// ecossistema Go do santos-tech.com (auth JWT compartilhada, Coolify,
// observabilidade).
//
// Exceção à regra 6 do CLAUDE.md (sqlc para acesso a banco): este serviço
// toca o Postgres compartilhado para UMA ÚNICA query (SELECT role FROM
// users WHERE id=$1, em requireAdmin/server.go) — todo o domínio de negócio
// (hits, keywords, estado do scan) vive no Elasticsearch, acessado via HTTP
// puro (elastic.go), exatamente como no dotfy-scanner original. Não há
// justificativa para versionar schema/queries sqlc por uma única query de
// autorização; a exigência de sqlc não se aplica aqui.
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

	"github.com/redis/go-redis/v9"
	"github.com/santos-tech/golog"
)

// revalidateInterval é de quanto em quanto tempo as chaves já achadas são
// testadas de novo automaticamente, sem precisar re-buscar no GitHub.
const revalidateInterval = 6 * time.Hour

func main() {
	golog.InitLogging()
	golog.InitSentry("secrets-go")
	defer golog.FlushSentry()

	cfg := LoadConfig()
	bootCtx := context.Background()

	db, err := newDB(bootCtx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("falha ao conectar no Postgres", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	registerDBMetrics(db)

	// REDIS_URL é opcional (ver config.go) — só usada pelo ipBanCheck (ban
	// global por IP). Sem ela, o middleware fica fail-open.
	var rdb *redis.Client
	if cfg.RedisURL != "" {
		opt, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			slog.Error("REDIS_URL inválida", "err", err)
			os.Exit(1)
		}
		rdb = redis.NewClient(opt)
		defer rdb.Close()
	}

	gh := NewGitHubClient(cfg.GitHubTokens)
	es := NewElasticClient(cfg.ElasticsearchURL)
	if err := es.EnsurePresetsIndex(context.Background()); err != nil {
		slog.Error("falha ao garantir índice de presets de keywords (segue sem travar o boot)", "err", err)
	}
	state := NewStateManager()
	hub := NewHub()
	if counts, err := es.KeywordStats(context.Background()); err != nil {
		slog.Error("falha ao carregar ranking de keywords do Elasticsearch (segue com ranking zerado)", "err", err)
	} else {
		hub.SeedKeywordCounts(counts)
	}
	dotfy := NewDotfyVerifier()
	verifiers := NewGenericVerifier()
	crawler := NewCrawler(gh, es, state, hub, dotfy, verifiers)
	revalidator := NewRevalidator(es, dotfy, verifiers, hub, state)

	srv := NewServer(cfg, db, rdb, crawler, state, es, hub, revalidator)

	// Contexto cancelado em SIGINT/SIGTERM: derruba o loop de revalidação
	// periódica e dispara o graceful shutdown do servidor HTTP.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go runPeriodicRevalidation(ctx, revalidator, state)

	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second, // anti slow-loris
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second, // generoso — o SSE de /api/events fica com a conexão aberta
		IdleTimeout:       30 * time.Second,
	}

	go func() {
		slog.Info("secrets-go ouvindo", "port", cfg.Port)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("erro no servidor", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("sinal recebido, encerrando graciosamente")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("falha no graceful shutdown", "err", err)
	}
}

// runPeriodicRevalidation refaz a verificação ativa de todos os hits já
// achados a cada revalidateInterval — pra "achei uma chave ativa" não virar
// uma foto congelada pra sempre se ela for revogada depois. Respeita
// AutoRevalidate (liga/desliga) e AutoRevalidateMaxRuns (0 = sem limite) —
// controláveis em tempo real via /api/settings, sem precisar reiniciar.
// Cancelável pelo mesmo ctx do graceful shutdown do servidor HTTP.
func runPeriodicRevalidation(ctx context.Context, r *Revalidator, state *StateManager) {
	ticker := time.NewTicker(revalidateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s := state.Get()
			if !s.AutoRevalidate {
				continue
			}
			if s.AutoRevalidateMaxRuns > 0 && s.AutoRevalidateRunCount >= s.AutoRevalidateMaxRuns {
				continue // limite atingido — sobe o limite ou desliga/liga pra resetar
			}
			r.Run(ctx)
			state.Update(func(s *StateData) { s.AutoRevalidateRunCount++ })
		}
	}
}
