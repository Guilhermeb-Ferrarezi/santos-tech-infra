package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	agentdb "github.com/santos-tech/agent/db"
)

// Teste de integração contra Postgres REAL (go build/test não valida SQL — já
// derrubou produção uma vez). Só roda com AGENT_TEST_DATABASE_URL apontando pra um
// banco DESCARTÁVEL (ele apaga as tabelas do agent-go):
//
//	docker run -d --rm --name pg -e POSTGRES_PASSWORD=teste -p 55439:5432 postgres:16-alpine
//	AGENT_TEST_DATABASE_URL=postgres://postgres:teste@localhost:55439/postgres go test -run PG ./...
//
// Simula o caminho de produção: tabela de uso no formato ANTIGO com um evento
// "raw" legado, e aí a migração nova roda (duas vezes — tem que ser idempotente).
func TestPGMigracaoUsoPorOrigemECobranca(t *testing.T) {
	url := os.Getenv("AGENT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("sem AGENT_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	exec(`DROP TABLE IF EXISTS claude_billing, claude_usage_events, claude_design_shares, api_keys,
		claude_push_tokens, claude_credentials, claude_messages, claude_conversations, users CASCADE`)
	// users é do api-go; claude_usage_events no formato de antes desta mudança.
	exec(`CREATE TABLE users (id BIGSERIAL PRIMARY KEY, role SMALLINT NOT NULL DEFAULT 1)`)
	exec(`CREATE TABLE claude_usage_events (
		id BIGSERIAL PRIMARY KEY, source TEXT NOT NULL, task TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '', conversation_id UUID,
		total_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0, input_tokens BIGINT NOT NULL DEFAULT 0,
		output_tokens BIGINT NOT NULL DEFAULT 0, cache_read_tokens BIGINT NOT NULL DEFAULT 0,
		cache_write_tokens BIGINT NOT NULL DEFAULT 0, duration_ms BIGINT NOT NULL DEFAULT 0,
		is_error BOOLEAN NOT NULL DEFAULT false, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	exec(`INSERT INTO claude_usage_events (source, task, total_cost_usd, created_at)
		VALUES ('generate', 'raw', 1.5, now() - interval '2 days')`)

	for i := 0; i < 2; i++ {
		if err := migrate(ctx, pool); err != nil {
			t.Fatalf("migração (rodada %d): %v", i+1, err)
		}
	}

	s := &Server{q: agentdb.New(pool), cfg: Config{EncryptionKey: "k"}}
	s.recordUsage(ctx, usageMeta{source: "generate", task: "raw", origin: "bot", billing: billingAPIKey, model: "sonnet"}, usageFields{TotalCostUSD: 0.2, InputTokens: 10, OutputTokens: 5})
	s.recordUsage(ctx, usageMeta{source: "generate", task: "raw", origin: "bot", model: "sonnet"}, usageFields{TotalCostUSD: 0.1})
	s.recordUsage(ctx, usageMeta{source: "generate", task: "raw", origin: "posaula", model: "sonnet"}, usageFields{TotalCostUSD: 0.4})
	s.recordUsage(ctx, usageMeta{source: "session", model: "opus"}, usageFields{TotalCostUSD: 2})
	s.recordUsage(ctx, usageMeta{source: "generate", task: "email", model: "sonnet"}, usageFields{TotalCostUSD: 0.05})
	// Evento de 10 dias atrás: entra nos 30 dias, fora da semana.
	exec(`INSERT INTO claude_usage_events (source, task, origin, total_cost_usd, created_at)
		VALUES ('generate', 'raw', 'bot', 1, now() - interval '10 days')`)

	now := time.Now()
	rows, err := s.q.UsageByOrigin(ctx, agentdb.UsageByOriginParams{Since: tstz(now.AddDate(0, 0, -30)), WeekSince: tstz(now.AddDate(0, 0, -7))})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]agentdb.UsageByOriginRow{}
	for _, r := range rows {
		got[r.OriginKey] = r
	}
	near := func(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }
	bot := got["bot"]
	if bot.Calls != 3 || !near(bot.CostUsd, 1.3) || bot.WeekCalls != 2 || !near(bot.WeekCostUsd, 0.3) || bot.ApiCalls != 1 || !near(bot.ApiCostUsd, 0.2) {
		t.Fatalf("bot errado: %+v", bot)
	}
	if r := got["raw"]; r.Calls != 1 || !near(r.CostUsd, 1.5) {
		t.Fatalf("legado raw errado: %+v", r)
	}
	if r := got["posaula"]; r.Calls != 1 || r.ApiCalls != 0 {
		t.Fatalf("posaula errado: %+v", r)
	}
	if r := got["sessao"]; r.Calls != 1 || !near(r.CostUsd, 2) {
		t.Fatalf("sessão errada: %+v", r)
	}
	if r := got["email"]; r.Calls != 1 {
		t.Fatalf("email (task, sem origem) errado: %+v", r)
	}

	sum, err := s.q.UsageSummary(ctx, tstz(now.AddDate(0, 0, -7)))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Calls != 6 || !near(sum.ApiCostUsd, 0.2) {
		t.Fatalf("resumo da semana errado: %+v", sum)
	}
	since, err := s.q.UsageSince(ctx)
	if err != nil || !since.Valid || now.Sub(since.Time) < 9*24*time.Hour {
		t.Fatalf("since errado: %+v %v", since, err)
	}

	// O endpoint monta o JSON com os campos novos.
	rec := httptest.NewRecorder()
	s.handleUsage(rec, httptest.NewRequest(http.MethodGet, "/claude/usage", nil))
	var res usageResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &res) != nil {
		t.Fatalf("GET /claude/usage: %d %s", rec.Code, rec.Body)
	}
	if res.Since == nil || res.Week.Calls != 6 || !near(res.Week.APICostUSD, 0.2) || len(res.Origin) != 5 {
		t.Fatalf("resposta do /claude/usage errada: %s", rec.Body)
	}

	// Cobrança: linha única criada pela migração, ligar exige chave.
	st := pgBillingStore{q: s.q}
	b, err := st.get(ctx)
	if err != nil || b.BotUsesAPIKey || len(b.APIKeyEnc) != 0 {
		t.Fatalf("estado inicial errado: %+v %v", b, err)
	}
	if n, err := st.setBotUsesAPIKey(ctx, true); err != nil || n != 0 {
		t.Fatalf("ligar sem chave devia afetar 0 linhas: n=%d err=%v", n, err)
	}
	if err := st.saveKey(ctx, []byte("cifra"), "1234"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.setBotUsesAPIKey(ctx, true); err != nil || n != 1 {
		t.Fatalf("ligar com chave: n=%d err=%v", n, err)
	}
	if err := st.clearKey(ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := st.get(ctx); b.BotUsesAPIKey || len(b.APIKeyEnc) != 0 || b.Hint != "" {
		t.Fatalf("apagar devia limpar e desligar: %+v", b)
	}
}
