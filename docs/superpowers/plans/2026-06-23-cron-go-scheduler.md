# cron-go Scheduler — Implementation Plan (Plano 1: backend)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Construir o serviço Go `cron-go` — um agendador de cron jobs programáveis em runtime que dispara endpoints HTTP do ecossistema (catálogo curado + HTTP cru opcional), com histórico de execução e API REST admin.

**Architecture:** Serviço HTTP stdlib em `package main` (flat), Postgres via `pgx/v5` + sqlc, sem framework. Um *scheduler* (tick loop) acorda periodicamente, reivindica jobs vencidos com `FOR UPDATE SKIP LOCKED` e os passa pro *dispatcher*, que valida allowlist, injeta o Bearer da conta de serviço, bate no alvo com timeout/retry e grava o resultado em `cron_runs`. A API REST (CRUD + histórico + disparo manual) é consumida pela UI do dashboard (Plano 2).

**Tech Stack:** Go 1.25, `net/http` stdlib, `pgx/v5` + sqlc (`pgx/v5` driver), `robfig/cron/v3` (parser de cron expression), `prometheus/client_golang`, `github.com/santos-tech/golog` (logging compartilhado). Sem Redis no MVP (rate-limit admin usa Postgres ou é dispensado nas rotas admin; ver Task 6).

## Global Constraints

- **Go 1.25**, `package main` flat (espelha `apps/payments-go` e `apps/api-go`).
- **Pré-commit obrigatório** (rodar e passar antes de CADA commit, a partir do diretório `apps/cron-go`): `gofmt -l .` (saída vazia) · `go vet ./...` · `go build ./...` · `go test ./...`. O binário `go` está em `~/.local/bin` — use `PATH=$PATH:$HOME/.local/bin` quando necessário.
- **sqlc obrigatório**: nenhum `pool.Query/Exec/QueryRow` inline em handlers. Todo SQL em `db/query/*.sql`, schema em `db/schema.sql`, código gerado em `db/` (`package db`, `sql_package: "pgx/v5"`). Geração: `/home/guilherme/.local/bin/sqlc generate` na raiz do serviço.
- **Logging**: `golog.InitLogging()` no `main`, `golog.RequestLogger(...)` como camada mais externa do handler raiz (mesmo uso de `payments-go`).
- **Operacionais fora de auth e rate-limit**: `/health` (liveness, não toca deps, loga DEBUG), `/ready` (pinga Postgres, 200/503), `/metrics` (Prometheus). `/health` e `/ready` não logam INFO.
- **Graceful shutdown**: `&http.Server{}` com `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`; `signal.NotifyContext(SIGINT, SIGTERM)` → `srv.Shutdown(ctx)`; fecha pool do Postgres e cancela o tick loop.
- **Rate-limit prefixado** por serviço se usado: `cron-go:rl:<rota>:<ip>`.
- **Fuso**: jobs guardam `timezone` (default `America/Sao_Paulo`); `next_run_at` é sempre **UTC**.
- **Erros HTTP**: `{ "code", "message" }` + status, `message` em português (convenção do ecossistema).
- **Auth admin**: toda rota `/cron/*` (exceto operacionais) exige sessão válida com papel **Admin** (role `3`). Ver Task 6.
- **Dispatcher → alvo**: Bearer da conta de serviço via env `CRON_SERVICE_PAT`; allowlist de hosts sempre validada; HTTP cru desligado salvo `CRON_ALLOW_RAW_HTTP=1`.

---

### Task 1: Scaffold do serviço + endpoints operacionais + graceful shutdown

Entrega: o serviço compila, sobe, responde `/health` (200 sem tocar deps), `/ready` (200 se Postgres pinga, 503 senão) e `/metrics` (Prometheus). Shutdown limpo em SIGTERM.

**Files:**
- Create: `apps/cron-go/go.mod`
- Create: `apps/cron-go/main.go`
- Create: `apps/cron-go/config.go`
- Create: `apps/cron-go/server.go`
- Create: `apps/cron-go/db.go`
- Create: `apps/cron-go/metrics.go`
- Create: `apps/cron-go/errors.go`
- Create: `apps/cron-go/server_test.go`

**Interfaces:**
- Produces:
  - `type Config struct { Port string; DatabaseURL string; Production bool; ServicePAT string; AllowRawHTTP bool; HostAllowlist []string }`
  - `func LoadConfig() Config`
  - `type Server struct { cfg Config; db *pgxpool.Pool; q *db.Queries }` (campo `q` adicionado na Task 3)
  - `func NewServer(cfg Config, pool *pgxpool.Pool) *Server`
  - `func (s *Server) Routes() http.Handler` (mux raiz, já embrulhado por `golog.RequestLogger`)
  - `func newDB(ctx context.Context, url string) (*pgxpool.Pool, error)`
  - `func writeError(w http.ResponseWriter, status int, code, message string)`
  - `func writeJSON(w http.ResponseWriter, status int, v any)`

- [ ] **Step 1: Inicializar o módulo Go**

```bash
cd apps/cron-go
PATH=$PATH:$HOME/.local/bin go mod init github.com/santos-tech/cron-go
PATH=$PATH:$HOME/.local/bin go get github.com/jackc/pgx/v5@latest \
  github.com/prometheus/client_golang@latest \
  github.com/santos-tech/golog@latest \
  github.com/robfig/cron/v3@latest
```

- [ ] **Step 2: Escrever `config.go`**

```go
package main

import (
	"os"
	"strings"
)

type Config struct {
	Port         string
	DatabaseURL  string
	Production   bool
	ServicePAT   string   // Bearer usado pelo dispatcher ao chamar alvos
	AllowRawHTTP bool     // habilita action_kind="http" com URL livre
	HostAllowlist []string // sufixos de host permitidos (ex.: ".santos-tech.com")
}

func LoadConfig() Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3337"
	}
	allow := os.Getenv("CRON_HOST_ALLOWLIST")
	if allow == "" {
		allow = ".santos-tech.com"
	}
	parts := strings.Split(allow, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return Config{
		Port:          port,
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		Production:    os.Getenv("NODE_ENV") == "production",
		ServicePAT:    os.Getenv("CRON_SERVICE_PAT"),
		AllowRawHTTP:  os.Getenv("CRON_ALLOW_RAW_HTTP") == "1",
		HostAllowlist: parts,
	}
}
```

- [ ] **Step 3: Escrever `errors.go`**

```go
package main

import (
	"encoding/json"
	"net/http"
)

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Code: code, Message: message})
}
```

- [ ] **Step 4: Escrever `db.go`** (pool pgx; espelha `apps/payments-go/db.go` — `pgxpool.New`, `QueryExecModeExec` para compat PgBouncer)

```go
package main

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newDB(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	// PgBouncer (transaction mode) não suporta prepared statements no servidor.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	return pgxpool.NewWithConfig(ctx, cfg)
}
```

- [ ] **Step 5: Escrever `metrics.go`** (registra coletor do pool; espelha `payments-go/metrics.go`)

```go
package main

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

func registerDBMetrics(pool *pgxpool.Pool) {
	prometheus.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{Name: "cron_go_db_pool_total_conns", Help: "Total de conexões no pool."},
		func() float64 { return float64(pool.Stat().TotalConns()) },
	))
}
```

- [ ] **Step 6: Escrever `server.go`** (mux raiz, handlers operacionais, embrulho de logging)

```go
package main

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/santos-tech/golog"
)

type Server struct {
	cfg Config
	db  *pgxpool.Pool
	// q *db.Queries  // adicionado na Task 3
}

func NewServer(cfg Config, pool *pgxpool.Pool) *Server {
	return &Server{cfg: cfg, db: pool}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.Handle("GET /metrics", promhttp.Handler())
	// rotas /cron/* adicionadas nas Tasks 4–9
	return golog.RequestLogger(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2_000_000_000) // 2s
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "Postgres indisponível")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
```

- [ ] **Step 7: Escrever o teste `server_test.go`**

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHealthOK(t *testing.T) {
	s := &Server{cfg: Config{}, db: (*pgxpool.Pool)(nil)}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d", rec.Code)
	}
}
```

- [ ] **Step 8: Rodar o teste e ver passar**

Run: `cd apps/cron-go && PATH=$PATH:$HOME/.local/bin go test -run TestHealthOK ./...`
Expected: PASS

- [ ] **Step 9: Escrever `main.go`** (boot + shutdown; espelha `payments-go/main.go`)

```go
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

	"github.com/santos-tech/golog"
)

func main() {
	golog.InitLogging()
	cfg := LoadConfig()
	ctx := context.Background()

	pool, err := newDB(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("falha ao conectar no Postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := migrate(ctx, pool); err != nil { // migrate() criado na Task 2
		slog.Error("falha na migração", "err", err)
		os.Exit(1)
	}
	registerDBMetrics(pool)

	srv := NewServer(cfg, pool)
	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// scheduler iniciado na Task 7: go srv.RunScheduler(schedCtx)

	go func() {
		slog.Info("cron-go ouvindo", "port", cfg.Port)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("servidor HTTP parou", "err", err)
			os.Exit(1)
		}
	}()

	<-rootCtx.Done()
	slog.Info("shutdown iniciado")
	shutCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		slog.Error("shutdown com erro", "err", err)
	}
}
```

> Nota: `main.go` referencia `migrate()` (Task 2). Até a Task 2, comente a chamada de `migrate` para `go build` passar, ou implemente as duas tasks na mesma sessão antes de buildar o `main`.

- [ ] **Step 10: gofmt + vet + build + test**

Run: `cd apps/cron-go && PATH=$PATH:$HOME/.local/bin sh -c 'gofmt -l . && go vet ./... && go build ./... && go test ./...'`
Expected: `gofmt` sem saída; build e test OK.

- [ ] **Step 11: Commit**

```bash
git add apps/cron-go/
git commit -m "feat(cron-go): scaffold do serviço com /health /ready /metrics e graceful shutdown"
```

---

### Task 2: Schema + sqlc (cron_jobs, cron_runs) + migração idempotente

Entrega: tabelas `cron_jobs` e `cron_runs` criadas no boot via `migrate()` idempotente; sqlc gera o `package db`.

**Files:**
- Create: `apps/cron-go/db/schema.sql`
- Create: `apps/cron-go/db/query/jobs.sql`
- Create: `apps/cron-go/db/query/runs.sql`
- Create: `apps/cron-go/sqlc.yaml`
- Create: `apps/cron-go/migrate.go`
- Generated (sqlc): `apps/cron-go/db/db.go`, `apps/cron-go/db/models.go`, `apps/cron-go/db/jobs.sql.go`, `apps/cron-go/db/runs.sql.go`

**Interfaces:**
- Consumes: `newDB` (Task 1).
- Produces:
  - `func migrate(ctx context.Context, pool *pgxpool.Pool) error`
  - `db.Queries` com (entre outros, nomes exatos usados adiante):
    `CreateJob`, `GetJob`, `ListJobs`, `UpdateJob`, `DeleteJob`, `SetJobEnabled`,
    `ClaimDueJobs` (`:many`, `FOR UPDATE SKIP LOCKED`), `UpdateJobAfterRun`,
    `CreateRun`, `FinishRun`, `ListRunsByJob`, `HasRunningRun`.

- [ ] **Step 1: Escrever `db/schema.sql`**

```sql
CREATE TABLE IF NOT EXISTS cron_jobs (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          TEXT        NOT NULL,
    description   TEXT        NOT NULL DEFAULT '',
    schedule_cron TEXT        NOT NULL,
    timezone      TEXT        NOT NULL DEFAULT 'America/Sao_Paulo',
    enabled       BOOLEAN     NOT NULL DEFAULT TRUE,
    action_kind   TEXT        NOT NULL CHECK (action_kind IN ('catalog','http')),
    action_ref    TEXT        NOT NULL DEFAULT '',
    http_method   TEXT        NOT NULL DEFAULT '',
    http_url      TEXT        NOT NULL DEFAULT '',
    http_headers  JSONB       NOT NULL DEFAULT '{}'::jsonb,
    http_body     TEXT        NOT NULL DEFAULT '',
    params        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    timeout_secs  INT         NOT NULL DEFAULT 30,
    max_retries   INT         NOT NULL DEFAULT 3,
    next_run_at   TIMESTAMPTZ,
    last_run_at   TIMESTAMPTZ,
    created_by    TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_cron_jobs_due
    ON cron_jobs (next_run_at) WHERE enabled = TRUE;

CREATE TABLE IF NOT EXISTS cron_runs (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id           BIGINT      NOT NULL REFERENCES cron_jobs(id) ON DELETE CASCADE,
    status           TEXT        NOT NULL CHECK (status IN ('running','success','failed','skipped_overlap')),
    attempt          INT         NOT NULL DEFAULT 1,
    http_status      INT,
    response_excerpt TEXT        NOT NULL DEFAULT '',
    error            TEXT        NOT NULL DEFAULT '',
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_cron_runs_job ON cron_runs (job_id, started_at DESC);
```

- [ ] **Step 2: Escrever `migrate.go`** (executa o schema; idempotente via `IF NOT EXISTS`)

```go
package main

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed db/schema.sql
var schemaSQL string

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schemaSQL)
	return err
}
```

- [ ] **Step 3: Escrever `db/query/jobs.sql`**

```sql
-- name: CreateJob :one
INSERT INTO cron_jobs (name, description, schedule_cron, timezone, enabled,
    action_kind, action_ref, http_method, http_url, http_headers, http_body,
    params, timeout_secs, max_retries, next_run_at, created_by)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
RETURNING *;

-- name: GetJob :one
SELECT * FROM cron_jobs WHERE id = $1;

-- name: ListJobs :many
SELECT * FROM cron_jobs ORDER BY created_at DESC;

-- name: UpdateJob :one
UPDATE cron_jobs SET
    name=$2, description=$3, schedule_cron=$4, timezone=$5,
    action_kind=$6, action_ref=$7, http_method=$8, http_url=$9,
    http_headers=$10, http_body=$11, params=$12, timeout_secs=$13,
    max_retries=$14, next_run_at=$15, updated_at=now()
WHERE id=$1
RETURNING *;

-- name: SetJobEnabled :exec
UPDATE cron_jobs SET enabled=$2, next_run_at=$3, updated_at=now() WHERE id=$1;

-- name: DeleteJob :exec
DELETE FROM cron_jobs WHERE id=$1;

-- name: ClaimDueJobs :many
SELECT * FROM cron_jobs
WHERE enabled = TRUE AND next_run_at IS NOT NULL AND next_run_at <= now()
ORDER BY next_run_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: UpdateJobAfterRun :exec
UPDATE cron_jobs SET last_run_at=now(), next_run_at=$2, updated_at=now() WHERE id=$1;
```

- [ ] **Step 4: Escrever `db/query/runs.sql`**

```sql
-- name: CreateRun :one
INSERT INTO cron_runs (job_id, status, attempt) VALUES ($1,$2,$3) RETURNING *;

-- name: FinishRun :exec
UPDATE cron_runs SET status=$2, http_status=$3, response_excerpt=$4, error=$5,
    attempt=$6, finished_at=now()
WHERE id=$1;

-- name: ListRunsByJob :many
SELECT * FROM cron_runs WHERE job_id=$1 ORDER BY started_at DESC LIMIT $2;

-- name: HasRunningRun :one
SELECT EXISTS(
    SELECT 1 FROM cron_runs WHERE job_id=$1 AND status='running'
) AS running;
```

- [ ] **Step 5: Escrever `sqlc.yaml`** (espelha `payments-go/sqlc.yaml`)

```yaml
version: "2"
sql:
  - engine: "postgresql"
    schema: "db/schema.sql"
    queries: "db/query"
    gen:
      go:
        package: "db"
        out: "db"
        sql_package: "pgx/v5"
        emit_json_tags: true
        emit_pointers_for_null_types: true
```

- [ ] **Step 6: Gerar o código sqlc**

Run: `cd apps/cron-go && /home/guilherme/.local/bin/sqlc generate`
Expected: cria `db/db.go`, `db/models.go`, `db/jobs.sql.go`, `db/runs.sql.go` sem erro.

- [ ] **Step 7: Plugar `db.Queries` no `Server`** — em `server.go`, adicionar import `"github.com/santos-tech/cron-go/db"`, campo `q *db.Queries` no struct, e em `NewServer`: `return &Server{cfg: cfg, db: pool, q: db.New(pool)}`. Descomentar a chamada `migrate` no `main.go`.

- [ ] **Step 8: gofmt + vet + build + test**

Run: `cd apps/cron-go && PATH=$PATH:$HOME/.local/bin sh -c 'gofmt -l . && go vet ./... && go build ./... && go test ./...'`
Expected: tudo OK.

- [ ] **Step 9: Commit**

```bash
git add apps/cron-go/
git commit -m "feat(cron-go): schema cron_jobs/cron_runs, queries sqlc e migração idempotente"
```

---

### Task 3: Cálculo de next_run_at (parser de cron + fuso → UTC)

Entrega: função pura que, dado `schedule_cron` + `timezone` + um instante de referência, devolve o próximo disparo em UTC. 100% testável sem deps.

**Files:**
- Create: `apps/cron-go/schedule.go`
- Create: `apps/cron-go/schedule_test.go`

**Interfaces:**
- Produces:
  - `func nextRun(cronExpr, tz string, after time.Time) (time.Time, error)` — devolve UTC.
  - `func validateCron(cronExpr, tz string) error` — usado pela validação de criação (Task 4).

- [ ] **Step 1: Escrever o teste `schedule_test.go`**

```go
package main

import (
	"testing"
	"time"
)

func TestNextRunDailySaoPaulo(t *testing.T) {
	// "todo dia às 09:00" em America/Sao_Paulo (UTC-3) = 12:00 UTC.
	after := time.Date(2026, 6, 23, 13, 0, 0, 0, time.UTC) // 10:00 BRT — já passou das 9h
	got, err := nextRun("0 9 * * *", "America/Sao_Paulo", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC) // 9h BRT do dia seguinte
	if !got.Equal(want) {
		t.Fatalf("esperava %s, veio %s", want, got)
	}
}

func TestValidateCronRejectsGarbage(t *testing.T) {
	if err := validateCron("not a cron", "America/Sao_Paulo"); err == nil {
		t.Fatal("esperava erro para cron inválido")
	}
	if err := validateCron("0 9 * * *", "Mars/Phobos"); err == nil {
		t.Fatal("esperava erro para timezone inválida")
	}
}
```

- [ ] **Step 2: Rodar o teste e ver falhar**

Run: `cd apps/cron-go && PATH=$PATH:$HOME/.local/bin go test -run 'TestNextRun|TestValidateCron' ./...`
Expected: FAIL (`nextRun`/`validateCron` não definidos).

- [ ] **Step 3: Escrever `schedule.go`**

```go
package main

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// parser padrão de 5 campos (min hora dia mês dia-da-semana).
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func nextRun(cronExpr, tz string, after time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Time{}, fmt.Errorf("timezone inválida: %w", err)
	}
	sched, err := cronParser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, fmt.Errorf("cron inválido: %w", err)
	}
	// Calcula no fuso do job e devolve em UTC.
	next := sched.Next(after.In(loc))
	return next.UTC(), nil
}

func validateCron(cronExpr, tz string) error {
	_, err := nextRun(cronExpr, tz, time.Unix(0, 0).UTC())
	return err
}
```

- [ ] **Step 4: Rodar o teste e ver passar**

Run: `cd apps/cron-go && PATH=$PATH:$HOME/.local/bin go test -run 'TestNextRun|TestValidateCron' ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add apps/cron-go/schedule.go apps/cron-go/schedule_test.go
git commit -m "feat(cron-go): cálculo de next_run_at via cron expression com fuso → UTC"
```

---

### Task 4: Catálogo de ações curado + GET /cron/catalog

Entrega: registro Go de ações curadas e endpoint que as lista. O catálogo mapeia `action_ref` → `{ método, host, path }` usados pelo dispatcher (Task 5).

**Files:**
- Create: `apps/cron-go/catalog.go`
- Create: `apps/cron-go/catalog_test.go`
- Create: `apps/cron-go/handlers_catalog.go`
- Modify: `apps/cron-go/server.go` (registrar rota)

**Interfaces:**
- Produces:
  - `type CatalogAction struct { ID string; Label string; Method string; Host string; Path string }`
  - `var Catalog map[string]CatalogAction`
  - `func lookupCatalog(ref string) (CatalogAction, bool)`
  - `func (s *Server) handleListCatalog(w http.ResponseWriter, r *http.Request)`

- [ ] **Step 1: Escrever o teste `catalog_test.go`**

```go
package main

import "testing"

func TestCatalogLookupKnown(t *testing.T) {
	a, ok := lookupCatalog("payments.gerar-cobrancas-mes")
	if !ok {
		t.Fatal("esperava encontrar a ação do catálogo")
	}
	if a.Method == "" || a.Host == "" || a.Path == "" {
		t.Fatalf("ação incompleta: %+v", a)
	}
}

func TestCatalogLookupUnknown(t *testing.T) {
	if _, ok := lookupCatalog("nao.existe"); ok {
		t.Fatal("não deveria encontrar ação inexistente")
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/cron-go && PATH=$PATH:$HOME/.local/bin go test -run TestCatalog ./...`
Expected: FAIL (`lookupCatalog` não definido).

- [ ] **Step 3: Escrever `catalog.go`**

```go
package main

// CatalogAction é uma ação curada que um job pode disparar. Host/Path são fixos
// no código — o admin nunca digita URL no modo catálogo.
type CatalogAction struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Method string `json:"-"`
	Host   string `json:"-"`
	Path   string `json:"-"`
}

// Catalog é o registro curado. Ampliar aqui ao expor uma nova ação agendável.
var Catalog = map[string]CatalogAction{
	"payments.gerar-cobrancas-mes": {
		ID: "payments.gerar-cobrancas-mes", Label: "Gerar cobranças do mês",
		Method: "POST", Host: "payments.santos-tech.com", Path: "/internal/gerar-cobrancas",
	},
	"email.relatorio-semanal": {
		ID: "email.relatorio-semanal", Label: "Enviar relatório semanal",
		Method: "POST", Host: "mails.santos-tech.com", Path: "/internal/relatorio-semanal",
	},
}

func lookupCatalog(ref string) (CatalogAction, bool) {
	a, ok := Catalog[ref]
	return a, ok
}
```

> As rotas `/internal/*` dos alvos são placeholders de exemplo — ao ativar uma ação de verdade, confirme o endpoint real no serviço-alvo e ajuste `Host`/`Path`. Não invente: se o endpoint não existir, crie-o no serviço-alvo num PR próprio.

- [ ] **Step 4: Escrever `handlers_catalog.go`**

```go
package main

import "net/http"

func (s *Server) handleListCatalog(w http.ResponseWriter, r *http.Request) {
	items := make([]CatalogAction, 0, len(Catalog))
	for _, a := range Catalog {
		items = append(items, a)
	}
	writeJSON(w, http.StatusOK, map[string]any{"actions": items})
}
```

- [ ] **Step 5: Registrar a rota em `server.go`** — dentro de `Routes()`, antes do `return`:

```go
mux.HandleFunc("GET /cron/catalog", s.requireAdmin(s.handleListCatalog))
```

> `requireAdmin` é definido na Task 6. Até lá, registre temporariamente como `s.handleListCatalog` sem o wrapper para buildar; troque na Task 6.

- [ ] **Step 6: Rodar e ver passar + build**

Run: `cd apps/cron-go && PATH=$PATH:$HOME/.local/bin sh -c 'go test -run TestCatalog ./... && go build ./...'`
Expected: PASS + build OK.

- [ ] **Step 7: Commit**

```bash
git add apps/cron-go/catalog.go apps/cron-go/catalog_test.go apps/cron-go/handlers_catalog.go apps/cron-go/server.go
git commit -m "feat(cron-go): catálogo de ações curado + GET /cron/catalog"
```

---

### Task 5: Dispatcher (allowlist + Bearer + timeout) — sem o loop ainda

Entrega: função que recebe um job, resolve o alvo (catálogo ou HTTP cru), valida a allowlist, monta e dispara o request com timeout e Bearer, e devolve status/excerpt/erro. A validação de allowlist é testada isoladamente; a chamada HTTP é testada contra um `httptest.Server`.

**Files:**
- Create: `apps/cron-go/dispatch.go`
- Create: `apps/cron-go/dispatch_test.go`

**Interfaces:**
- Consumes: `CatalogAction`, `lookupCatalog` (Task 4); `db.CronJob` (Task 2); `Config` (Task 1).
- Produces:
  - `type dispatchResult struct { HTTPStatus int; Excerpt string; Err error }`
  - `func hostAllowed(host string, allowlist []string) bool`
  - `func (s *Server) buildTargetURL(job db.CronJob) (method, url string, err error)`
  - `func (s *Server) dispatch(ctx context.Context, job db.CronJob) dispatchResult`

- [ ] **Step 1: Escrever o teste `dispatch_test.go`**

```go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/santos-tech/cron-go/db"
)

func TestHostAllowed(t *testing.T) {
	allow := []string{".santos-tech.com"}
	cases := map[string]bool{
		"payments.santos-tech.com": true,
		"localhost":                false,
		"169.254.169.254":          false,
		"evil.com":                 false,
		"santos-tech.com.evil.com": false,
	}
	for host, want := range cases {
		if got := hostAllowed(host, allow); got != want {
			t.Errorf("hostAllowed(%q)=%v, queria %v", host, got, want)
		}
	}
}

func TestDispatchCatalogSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-pat" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	}))
	defer ts.Close()

	// Injeta uma ação de catálogo apontando para o servidor de teste, com allowlist liberada.
	Catalog["test.action"] = CatalogAction{ID: "test.action", Label: "t", Method: "POST", Host: hostOf(ts.URL), Path: "/"}
	defer delete(Catalog, "test.action")

	s := &Server{cfg: Config{ServicePAT: "test-pat", HostAllowlist: []string{hostOf(ts.URL)}}}
	job := db.CronJob{ActionKind: "catalog", ActionRef: "test.action", TimeoutSecs: 5}
	res := s.dispatch(context.Background(), job)
	if res.Err != nil || res.HTTPStatus != http.StatusOK {
		t.Fatalf("esperava sucesso 200, veio status=%d err=%v", res.HTTPStatus, res.Err)
	}
}
```

> `hostOf` é um helper de teste: extrai o host (com porta) de uma URL. Adicione no topo do arquivo de teste:
> ```go
> func hostOf(raw string) string { u, _ := url.Parse(raw); return u.Host }
> ```
> e o import `"net/url"`. Para o teste passar, a allowlist precisa casar host:porta do `httptest` — `hostAllowed` deve permitir match exato além de sufixo (ver implementação).

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/cron-go && PATH=$PATH:$HOME/.local/bin go test -run 'TestHostAllowed|TestDispatch' ./...`
Expected: FAIL.

- [ ] **Step 3: Escrever `dispatch.go`**

```go
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/santos-tech/cron-go/db"
)

type dispatchResult struct {
	HTTPStatus int
	Excerpt    string
	Err        error
}

// hostAllowed: bloqueia localhost/IP privado/link-local; permite match exato ou
// sufixo da allowlist (ex.: ".santos-tech.com").
func hostAllowed(host string, allowlist []string) bool {
	h := host
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i] // tira porta
	}
	if h == "" || h == "localhost" {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return false
		}
	}
	for _, suf := range allowlist {
		if suf == "" {
			continue
		}
		if h == strings.TrimPrefix(suf, ".") || strings.HasSuffix(h, suf) || host == suf {
			return true
		}
	}
	return false
}

func (s *Server) buildTargetURL(job db.CronJob) (method, rawURL string, err error) {
	switch job.ActionKind {
	case "catalog":
		a, ok := lookupCatalog(job.ActionRef)
		if !ok {
			return "", "", fmt.Errorf("ação de catálogo desconhecida: %s", job.ActionRef)
		}
		return a.Method, "https://" + a.Host + a.Path, nil
	case "http":
		if !s.cfg.AllowRawHTTP {
			return "", "", fmt.Errorf("HTTP cru desabilitado (CRON_ALLOW_RAW_HTTP)")
		}
		m := job.HttpMethod
		if m == "" {
			m = http.MethodPost
		}
		return m, job.HttpUrl, nil
	default:
		return "", "", fmt.Errorf("action_kind inválido: %s", job.ActionKind)
	}
}

func (s *Server) dispatch(ctx context.Context, job db.CronJob) dispatchResult {
	method, rawURL, err := s.buildTargetURL(job)
	if err != nil {
		return dispatchResult{Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return dispatchResult{Err: err}
	}
	if !hostAllowed(req.URL.Host, s.cfg.HostAllowlist) {
		return dispatchResult{Err: fmt.Errorf("host fora da allowlist: %s", req.URL.Host)}
	}
	if s.cfg.ServicePAT != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.ServicePAT)
	}
	req.Header.Set("Content-Type", "application/json")

	timeout := time.Duration(job.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return dispatchResult{Err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	res := dispatchResult{HTTPStatus: resp.StatusCode, Excerpt: string(body)}
	if resp.StatusCode >= 400 {
		res.Err = fmt.Errorf("alvo respondeu %d", resp.StatusCode)
	}
	return res
}
```

> Os campos gerados pelo sqlc para colunas `http_method`/`http_url` são `HttpMethod`/`HttpUrl`. Confirme os nomes em `db/models.go` após a Task 2 e ajuste se o sqlc gerar variação (ex.: `HTTPMethod`). Este plano assume `HttpMethod`/`HttpUrl`.

- [ ] **Step 4: Rodar e ver passar**

Run: `cd apps/cron-go && PATH=$PATH:$HOME/.local/bin go test -run 'TestHostAllowed|TestDispatch' ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add apps/cron-go/dispatch.go apps/cron-go/dispatch_test.go
git commit -m "feat(cron-go): dispatcher com allowlist de host, Bearer de serviço e timeout"
```

---

### Task 6: Guard de Admin + CRUD de jobs (`/cron/jobs`)

Entrega: middleware que exige sessão com papel Admin; endpoints CRUD que validam o cron e calculam `next_run_at` na criação/edição.

**Files:**
- Create: `apps/cron-go/auth.go`
- Create: `apps/cron-go/handlers_jobs.go`
- Create: `apps/cron-go/handlers_jobs_test.go`
- Modify: `apps/cron-go/server.go` (rotas + trocar wrappers temporários por `requireAdmin`)

**Interfaces:**
- Consumes: `validateCron`, `nextRun` (Task 3); `db.Queries` (Task 2); `writeError`/`writeJSON` (Task 1).
- Produces:
  - `func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc`
  - `func (s *Server) handleCreateJob(...)`, `handleListJobs`, `handleGetJob`, `handleUpdateJob`, `handleDeleteJob`
  - `type jobInput struct { ... }` (corpo da request)

- [ ] **Step 1: Escrever `auth.go`** — valida o token de sessão chamando a Auth API (`GET /auth/me` com o cookie repassado) e exige `role == 3`. Espelha como `mcp-go`/dashboard validam sessão.

```go
package main

import (
	"encoding/json"
	"net/http"
)

// requireAdmin repassa os cookies da request para a Auth API (/auth/me) e só
// segue se a resposta indicar papel Admin (role 3). Fail-closed.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.cfg.AuthMeURL, nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "auth_error", "falha ao checar sessão")
			return
		}
		req.Header.Set("Cookie", r.Header.Get("Cookie"))
		if h := r.Header.Get("Authorization"); h != "" {
			req.Header.Set("Authorization", h)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			writeError(w, http.StatusUnauthorized, "unauthorized", "sessão inválida")
			return
		}
		defer resp.Body.Close()
		var me struct {
			Role int `json:"role"`
		}
		if json.NewDecoder(resp.Body).Decode(&me) != nil || me.Role != 3 {
			writeError(w, http.StatusForbidden, "forbidden", "requer papel Admin")
			return
		}
		next(w, r)
	}
}
```

> Adicione `AuthMeURL string` ao `Config` (Task 1) lendo `AUTH_ME_URL` (default `https://api.santos-tech.com/auth/me`; no compose local, `http://api:3000/auth/me`).

- [ ] **Step 2: Escrever `handlers_jobs.go`** (CRUD; valida cron e calcula `next_run_at`)

```go
package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/santos-tech/cron-go/db"
)

type jobInput struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	ScheduleCron string          `json:"scheduleCron"`
	Timezone     string          `json:"timezone"`
	ActionKind   string          `json:"actionKind"`
	ActionRef    string          `json:"actionRef"`
	HTTPMethod   string          `json:"httpMethod"`
	HTTPURL      string          `json:"httpUrl"`
	HTTPHeaders  json.RawMessage `json:"httpHeaders"`
	HTTPBody     string          `json:"httpBody"`
	Params       json.RawMessage `json:"params"`
	TimeoutSecs  int32           `json:"timeoutSecs"`
	MaxRetries   int32           `json:"maxRetries"`
}

func (in *jobInput) defaults() {
	if in.Timezone == "" {
		in.Timezone = "America/Sao_Paulo"
	}
	if in.TimeoutSecs <= 0 {
		in.TimeoutSecs = 30
	}
	if in.MaxRetries <= 0 {
		in.MaxRetries = 3
	}
	if len(in.HTTPHeaders) == 0 {
		in.HTTPHeaders = []byte("{}")
	}
	if len(in.Params) == 0 {
		in.Params = []byte("{}")
	}
}

func (in jobInput) validate(allowRaw bool) (string, bool) {
	if in.Name == "" {
		return "name obrigatório", false
	}
	if in.ActionKind != "catalog" && in.ActionKind != "http" {
		return "actionKind deve ser catalog ou http", false
	}
	if in.ActionKind == "catalog" {
		if _, ok := lookupCatalog(in.ActionRef); !ok {
			return "ação de catálogo desconhecida", false
		}
	}
	if in.ActionKind == "http" && !allowRaw {
		return "HTTP cru desabilitado", false
	}
	if err := validateCron(in.ScheduleCron, in.Timezone); err != nil {
		return err.Error(), false
	}
	return "", true
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var in jobInput
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "JSON inválido")
		return
	}
	in.defaults()
	if msg, ok := in.validate(s.cfg.AllowRawHTTP); !ok {
		writeError(w, http.StatusBadRequest, "validation", msg)
		return
	}
	next, _ := nextRun(in.ScheduleCron, in.Timezone, time.Now().UTC())
	job, err := s.q.CreateJob(r.Context(), db.CreateJobParams{
		Name: in.Name, Description: in.Description, ScheduleCron: in.ScheduleCron,
		Timezone: in.Timezone, Enabled: true, ActionKind: in.ActionKind, ActionRef: in.ActionRef,
		HttpMethod: in.HTTPMethod, HttpUrl: in.HTTPURL, HttpHeaders: in.HTTPHeaders,
		HttpBody: in.HTTPBody, Params: in.Params, TimeoutSecs: in.TimeoutSecs,
		MaxRetries: in.MaxRetries, NextRunAt: pgTimestamp(next), CreatedBy: "",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "falha ao criar job")
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.q.ListJobs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "id inválido")
		return
	}
	job, err := s.q.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "job não encontrado")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "id inválido")
		return
	}
	if s.q.DeleteJob(r.Context(), id) != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "falha ao remover")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

> `pgTimestamp(t time.Time) pgtype.Timestamptz` é um helper. Adicione em `util.go` (criar):
> ```go
> package main
> import ("time"; "github.com/jackc/pgx/v5/pgtype")
> func pgTimestamp(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
> ```
> `handleUpdateJob` segue o mesmo molde de `handleCreateJob` chamando `s.q.UpdateJob` com `db.UpdateJobParams` (inclui `ID` + recalcula `NextRunAt`). Implemente-o espelhando o create.

- [ ] **Step 3: Escrever o teste `handlers_jobs_test.go`** (valida o `jobInput.validate`, puro, sem DB)

```go
package main

import "testing"

func TestJobInputValidate(t *testing.T) {
	good := jobInput{Name: "x", ActionKind: "catalog", ActionRef: "payments.gerar-cobrancas-mes", ScheduleCron: "0 9 * * *", Timezone: "America/Sao_Paulo"}
	if msg, ok := good.validate(false); !ok {
		t.Fatalf("esperava válido, veio: %s", msg)
	}
	bad := jobInput{Name: "x", ActionKind: "http", ScheduleCron: "0 9 * * *", Timezone: "America/Sao_Paulo"}
	if _, ok := bad.validate(false); ok {
		t.Fatal("esperava rejeitar HTTP cru com allowRaw=false")
	}
}
```

- [ ] **Step 4: Registrar rotas em `server.go`** — substituir o wrapper temporário do catálogo e adicionar:

```go
mux.HandleFunc("GET /cron/jobs", s.requireAdmin(s.handleListJobs))
mux.HandleFunc("POST /cron/jobs", s.requireAdmin(s.handleCreateJob))
mux.HandleFunc("GET /cron/jobs/{id}", s.requireAdmin(s.handleGetJob))
mux.HandleFunc("PATCH /cron/jobs/{id}", s.requireAdmin(s.handleUpdateJob))
mux.HandleFunc("DELETE /cron/jobs/{id}", s.requireAdmin(s.handleDeleteJob))
mux.HandleFunc("GET /cron/catalog", s.requireAdmin(s.handleListCatalog))
```

- [ ] **Step 5: Rodar testes + build**

Run: `cd apps/cron-go && PATH=$PATH:$HOME/.local/bin sh -c 'go test ./... && go build ./...'`
Expected: PASS + build OK.

- [ ] **Step 6: Commit**

```bash
git add apps/cron-go/
git commit -m "feat(cron-go): guard de Admin + CRUD de jobs com validação de cron"
```

---

### Task 7: Scheduler tick loop (claim + skip overlap + retry/backoff + recompute)

Entrega: loop que acorda a cada 30s, reivindica jobs vencidos numa transação com `FOR UPDATE SKIP LOCKED`, pula se já há run `running` (overlap), dispara com retry/backoff, grava `cron_runs` e recalcula `next_run_at` (sem catch-up).

**Files:**
- Create: `apps/cron-go/scheduler.go`
- Create: `apps/cron-go/scheduler_test.go`
- Modify: `apps/cron-go/main.go` (iniciar o loop)

**Interfaces:**
- Consumes: `db.Queries` (Task 2), `dispatch` (Task 5), `nextRun` (Task 3), `HasRunningRun`/`ClaimDueJobs`/`CreateRun`/`FinishRun`/`UpdateJobAfterRun`.
- Produces:
  - `func (s *Server) RunScheduler(ctx context.Context)`
  - `func (s *Server) runJobOnce(ctx context.Context, job db.CronJob)`
  - `func backoff(attempt int) time.Duration`

- [ ] **Step 1: Escrever o teste `scheduler_test.go`** (backoff puro)

```go
package main

import (
	"testing"
	"time"
)

func TestBackoffGrows(t *testing.T) {
	if backoff(1) >= backoff(2) || backoff(2) >= backoff(3) {
		t.Fatal("backoff deveria crescer com a tentativa")
	}
	if backoff(10) > 5*time.Minute {
		t.Fatal("backoff deveria ter teto")
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/cron-go && PATH=$PATH:$HOME/.local/bin go test -run TestBackoff ./...`
Expected: FAIL.

- [ ] **Step 3: Escrever `scheduler.go`**

```go
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/santos-tech/cron-go/db"
)

func backoff(attempt int) time.Duration {
	d := time.Duration(attempt*attempt) * time.Second
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}

// RunScheduler acorda a cada 30s e processa os jobs vencidos. Para no ctx.Done().
func (s *Server) RunScheduler(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

func (s *Server) tick(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("panic no tick do scheduler", "panic", rec)
		}
	}()
	// Transação para o claim com SKIP LOCKED — segura com múltiplas réplicas.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		slog.Error("tick: begin falhou", "err", err)
		return
	}
	qtx := db.New(tx)
	jobs, err := qtx.ClaimDueJobs(ctx, 20)
	if err != nil {
		_ = tx.Rollback(ctx)
		slog.Error("tick: claim falhou", "err", err)
		return
	}
	// Recalcula next_run_at já dentro da tx (evita re-claim no próximo tick).
	for _, job := range jobs {
		next, errN := nextRun(job.ScheduleCron, job.Timezone, time.Now().UTC())
		if errN != nil {
			slog.Error("cron inválido em job; pulando", "job", job.ID, "err", errN)
			continue
		}
		_ = qtx.UpdateJobAfterRun(ctx, db.UpdateJobAfterRunParams{ID: job.ID, NextRunAt: pgTimestamp(next)})
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Error("tick: commit falhou", "err", err)
		return
	}
	// Dispara fora da tx — chamadas HTTP não devem segurar locks.
	for _, job := range jobs {
		go s.runJobOnce(context.Background(), job)
	}
}

func (s *Server) runJobOnce(ctx context.Context, job db.CronJob) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("panic em runJobOnce", "job", job.ID, "panic", rec)
		}
	}()
	// Skip de sobreposição: já existe run 'running' para este job.
	running, _ := s.q.HasRunningRun(ctx, job.ID)
	if running {
		run, _ := s.q.CreateRun(ctx, db.CreateRunParams{JobID: job.ID, Status: "running", Attempt: 1})
		_ = s.q.FinishRun(ctx, db.FinishRunParams{
			ID: run.ID, Status: "skipped_overlap", Attempt: 1,
		})
		return
	}
	run, err := s.q.CreateRun(ctx, db.CreateRunParams{JobID: job.ID, Status: "running", Attempt: 1})
	if err != nil {
		slog.Error("falha ao criar run", "job", job.ID, "err", err)
		return
	}
	var last dispatchResult
	attempt := 1
	for ; attempt <= int(job.MaxRetries); attempt++ {
		last = s.dispatch(ctx, job)
		if last.Err == nil {
			break
		}
		if attempt < int(job.MaxRetries) {
			time.Sleep(backoff(attempt))
		}
	}
	status := "success"
	errStr := ""
	if last.Err != nil {
		status = "failed"
		errStr = last.Err.Error()
	}
	var httpStatus *int32
	if last.HTTPStatus != 0 {
		v := int32(last.HTTPStatus)
		httpStatus = &v
	}
	_ = s.q.FinishRun(ctx, db.FinishRunParams{
		ID: run.ID, Status: status, HttpStatus: httpStatus,
		ResponseExcerpt: redact(last.Excerpt), Error: errStr, Attempt: int32(attempt),
	})
}
```

> `redact(s string) string` reaproveita a regra de redação de credenciais (senha/token/secret → `***`). Se `golog` exportar um helper, use-o; senão adicione um `redact` simples em `util.go` que zera valores de chaves sensíveis. `HttpStatus` é `*int32` porque `http_status` é nullable (sqlc com `emit_pointers_for_null_types`). Confirme o tipo gerado em `db/runs.sql.go` e ajuste.

- [ ] **Step 4: Rodar e ver passar**

Run: `cd apps/cron-go && PATH=$PATH:$HOME/.local/bin go test -run TestBackoff ./...`
Expected: PASS

- [ ] **Step 5: Iniciar o loop no `main.go`** — após criar `srv` e antes do `ListenAndServe`, adicionar:

```go
schedCtx, cancelSched := context.WithCancel(rootCtx)
defer cancelSched()
go srv.RunScheduler(schedCtx)
```

- [ ] **Step 6: gofmt + vet + build + test**

Run: `cd apps/cron-go && PATH=$PATH:$HOME/.local/bin sh -c 'gofmt -l . && go vet ./... && go build ./... && go test ./...'`
Expected: tudo OK.

- [ ] **Step 7: Commit**

```bash
git add apps/cron-go/
git commit -m "feat(cron-go): scheduler com claim SKIP LOCKED, skip de overlap, retry/backoff e sem catch-up"
```

---

### Task 8: Pause/resume, disparo manual e histórico de runs

Entrega: endpoints `POST /cron/jobs/{id}/pause`, `/resume`, `/run` (disparo manual síncrono) e `GET /cron/jobs/{id}/runs`.

**Files:**
- Create: `apps/cron-go/handlers_runs.go`
- Create: `apps/cron-go/handlers_runs_test.go`
- Modify: `apps/cron-go/server.go` (rotas)

**Interfaces:**
- Consumes: `SetJobEnabled`, `ListRunsByJob`, `GetJob` (Task 2); `runJobOnce` (Task 7); `nextRun` (Task 3).
- Produces: `handlePauseJob`, `handleResumeJob`, `handleRunJob`, `handleListRuns`.

- [ ] **Step 1: Escrever `handlers_runs.go`**

```go
package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/santos-tech/cron-go/db"
)

func (s *Server) jobID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "id inválido")
		return 0, false
	}
	return id, true
}

func (s *Server) handlePauseJob(w http.ResponseWriter, r *http.Request) {
	id, ok := s.jobID(w, r)
	if !ok {
		return
	}
	if s.q.SetJobEnabled(r.Context(), db.SetJobEnabledParams{ID: id, Enabled: false, NextRunAt: pgtype.Timestamptz{}}) != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "falha ao pausar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (s *Server) handleResumeJob(w http.ResponseWriter, r *http.Request) {
	id, ok := s.jobID(w, r)
	if !ok {
		return
	}
	job, err := s.q.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "job não encontrado")
		return
	}
	next, _ := nextRun(job.ScheduleCron, job.Timezone, time.Now().UTC())
	if s.q.SetJobEnabled(r.Context(), db.SetJobEnabledParams{ID: id, Enabled: true, NextRunAt: pgTimestamp(next)}) != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "falha ao retomar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

func (s *Server) handleRunJob(w http.ResponseWriter, r *http.Request) {
	id, ok := s.jobID(w, r)
	if !ok {
		return
	}
	job, err := s.q.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "job não encontrado")
		return
	}
	s.runJobOnce(r.Context(), job) // síncrono: o admin vê o resultado no histórico
	writeJSON(w, http.StatusOK, map[string]string{"status": "executed"})
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := s.jobID(w, r)
	if !ok {
		return
	}
	runs, err := s.q.ListRunsByJob(r.Context(), db.ListRunsByJobParams{JobID: id, Limit: 50})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "falha ao listar runs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}
```

- [ ] **Step 2: Escrever o teste `handlers_runs_test.go`** (valida parse de id inválido → 400, sem DB)

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunJobBadID(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/cron/jobs/abc/run", nil)
	req.SetPathValue("id", "abc")
	rec := httptest.NewRecorder()
	s.handleRunJob(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("esperava 400, veio %d", rec.Code)
	}
}
```

- [ ] **Step 3: Registrar rotas em `server.go`**

```go
mux.HandleFunc("POST /cron/jobs/{id}/pause", s.requireAdmin(s.handlePauseJob))
mux.HandleFunc("POST /cron/jobs/{id}/resume", s.requireAdmin(s.handleResumeJob))
mux.HandleFunc("POST /cron/jobs/{id}/run", s.requireAdmin(s.handleRunJob))
mux.HandleFunc("GET /cron/jobs/{id}/runs", s.requireAdmin(s.handleListRuns))
```

- [ ] **Step 4: gofmt + vet + build + test**

Run: `cd apps/cron-go && PATH=$PATH:$HOME/.local/bin sh -c 'gofmt -l . && go vet ./... && go build ./... && go test ./...'`
Expected: tudo OK.

- [ ] **Step 5: Commit**

```bash
git add apps/cron-go/
git commit -m "feat(cron-go): pause/resume, disparo manual e histórico de execuções"
```

---

### Task 9: Empacotamento (Dockerfile, compose, CI, env, docs)

Entrega: o serviço builda em container, sobe no compose com healthcheck, entra no CI Go, tem `.env.example`, e está documentado (watch_paths anotado, llms.txt).

**Files:**
- Create: `infra/Dockerfile.cron-go`
- Modify: `infra/docker-compose.yml`
- Modify: `.github/workflows/go.yml`
- Create: `apps/cron-go/.env.example`
- Modify: `apps/api-go/llms.txt`

**Interfaces:** nenhuma nova (empacotamento).

- [ ] **Step 1: Escrever `infra/Dockerfile.cron-go`** — copie `infra/Dockerfile.payments-go` trocando o nome do app/binário e o `PORT`. Confira o conteúdo real:

Run: `cat infra/Dockerfile.payments-go`
Depois crie `infra/Dockerfile.cron-go` idêntico, substituindo `payments-go` → `cron-go` no contexto de build e no path do binário. (cron-go não copia `docs/openapi.yaml` — não precisa.)

- [ ] **Step 2: Adicionar o serviço no `infra/docker-compose.yml`** (espelha o bloco `payments-go`)

```yaml
  cron-go:
    build:
      context: ..
      dockerfile: infra/Dockerfile.cron-go
    ports:
      - "3337:3337"
    depends_on:
      pgbouncer:
        condition: service_healthy
      api:
        condition: service_healthy
    env_file:
      - ../.env
    environment:
      <<: *db-env
      PORT: "3337"
      AUTH_ME_URL: http://api:3000/auth/me
    healthcheck:
      test: ["CMD", "wget", "--spider", "-q", "http://127.0.0.1:3337/health"]
      interval: 10s
      timeout: 5s
      retries: 5
      start_period: 20s
    restart: unless-stopped
```

- [ ] **Step 3: Adicionar `cron-go` na matriz do `.github/workflows/go.yml`** — localize a lista de serviços (ex.: `matrix.service` ou os `working-directory`) e inclua `apps/cron-go`. Confira primeiro:

Run: `grep -n "payments-go\|matrix\|working-directory" .github/workflows/go.yml`
Depois replique a entrada de `payments-go` para `cron-go`.

- [ ] **Step 4: Escrever `apps/cron-go/.env.example`**

```bash
PORT=3337
DATABASE_URL=postgres://postgres:postgres@localhost:5432/santos_tech?sslmode=disable
NODE_ENV=development
# Bearer da conta de serviço usada pelo dispatcher ao chamar os alvos.
CRON_SERVICE_PAT=
# URL do /auth/me usada para validar sessão Admin.
AUTH_ME_URL=https://api.santos-tech.com/auth/me
# Hosts permitidos para o dispatcher (CSV de sufixos). Default: .santos-tech.com
CRON_HOST_ALLOWLIST=.santos-tech.com
# 1 habilita action_kind=http com URL livre (default 0: só catálogo).
CRON_ALLOW_RAW_HTTP=0
```

- [ ] **Step 5: Documentar no `apps/api-go/llms.txt`** — adicione uma seção curta para a API do cron-go (base URL, rotas `/cron/*`, auth Admin). Siga o formato das seções existentes do arquivo.

- [ ] **Step 6: Validar o build do container**

Run: `docker compose -f infra/docker-compose.yml build cron-go`
Expected: build conclui sem erro.

- [ ] **Step 7: Commit**

```bash
git add infra/Dockerfile.cron-go infra/docker-compose.yml .github/workflows/go.yml apps/cron-go/.env.example apps/api-go/llms.txt
git commit -m "chore(cron-go): Dockerfile, compose, CI, .env.example e docs"
```

---

## Pós-plano (fora deste plano, anotar para depois)

- **watch_paths na Coolify**: ao criar o app `cron-go` na Coolify, configurar
  `watch_paths` = `apps/cron-go/**` + `infra/Dockerfile.cron-go` (via `PATCH /api/v1/applications/{uuid}`). É passo de infra manual, não de código.
- **Conta de serviço + PAT**: criar a conta de serviço de menor privilégio e gerar o
  `st_...` PAT; setar `CRON_SERVICE_PAT` nos envs da Coolify.
- **Endpoints `/internal/*` dos alvos**: as ações do catálogo apontam para rotas que os
  serviços-alvo precisam expor de fato (e validar o Bearer da conta de serviço). Cada
  ação real é um PR no serviço-alvo.
- **Plano 2 — UI no dashboard** (`../org/dashboard`): `src/lib/cron.ts`, telas
  lista/form/histórico com `PageShell`, entrada no `nav.ts`, E2E smoke + mocks.

## Self-Review (preenchido)

- **Cobertura do spec**: dados (Task 2) ✓ · auth dois contextos (Task 5 dispatcher PAT + Task 6 Admin guard) ✓ · catálogo + HTTP cru flag (Tasks 4/5/6) ✓ · scheduler claim/overlap/retry/no-catch-up (Task 7) ✓ · CRUD + pause/resume + run manual + histórico (Tasks 6/8) ✓ · cron+fuso (Task 3) ✓ · padrões Go obrigatórios: shutdown/health/ready/metrics (Task 1), sqlc (Task 2), logging (Task 1), CI/compose/Dockerfile (Task 9) ✓. UI = Plano 2 (fora de escopo aqui, por decisão de scope check).
- **Placeholders**: nenhum "TBD/TODO"; pontos que dependem de inspeção real do código gerado (nomes de campos sqlc, conteúdo do Dockerfile/go.yml) estão marcados com instrução explícita de `Run:`/conferência, não como lacuna.
- **Consistência de tipos**: nomes de queries (`CreateJob`, `ClaimDueJobs`, `HasRunningRun`, `FinishRun`, `UpdateJobAfterRun`, `SetJobEnabled`, `ListRunsByJob`) usados de forma idêntica entre Task 2 (definição) e Tasks 6/7/8 (consumo); `dispatchResult`/`dispatch`/`hostAllowed`/`nextRun`/`pgTimestamp`/`requireAdmin` consistentes entre tasks.
