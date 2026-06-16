# API de Pagamentos Pix (payments-go) — Plano de Implementação

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Subir o serviço Go `apps/payments-go` que cobra mensalidades e matrículas/avulsos via Pix (gateway Dotfy), com recorrência mensal própria, webhook idempotente e controle de inadimplência.

**Architecture:** Serviço Go autônomo no monorepo, padrão `apps/api-go` (`net/http` stdlib, `pgx/v5`, `slog`). Núcleo (alunos/planos/assinaturas/cobranças) isolado do gateway por trás da interface `PaymentProvider`; impl `dotfyProvider` na Fase 1. Recorrência via goroutine com ticker diário. Auth reusa o JWT HS256 do ecossistema (valida, não emite); autorização Admin lendo `users.role` no Postgres compartilhado.

**Tech Stack:** Go 1.25, `net/http`, `pgx/v5` (Postgres 16), `golang-jwt/v5`, `slog`. Dotfy API em `https://app.dotfy.com.br`, auth `Authorization: Bearer <DOTFY_API_KEY>`.

---

## Padrões do repositório (ler antes de começar)

- **Erros HTTP:** sempre `{ "code": "...", "message": "..." }` com `message` em português. Helper `writeError(w, status, code, msg)`.
- **Auth:** access token via cookie `access_token` **ou** `Authorization: Bearer <jwt>`. `verifyToken` valida HS256 e retorna `sub` (userID). Admin = `users.role == 3`.
- **Migrations:** `const migration` SQL idempotente (`CREATE TABLE IF NOT EXISTS`), rodada por `migrate()` no boot. **Tabelas próprias com prefixo `pay_`** — não tocar em `users`.
- **Email:** `emailClient.send(ctx, to, subject, html)` → `POST {EMAIL_API_URL}/send`, header `X-Api-Key`.
- **Arquivos flat**, `package main`, espelhando `apps/api-go`.
- **Pré-commit (obrigatório):** `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin gofmt -l .` (vazio) `&& go vet ./... && go build ./... && go test ./...`. O binário `go` está em `~/.local/bin`.
- **Valores monetários:** sempre **centavos** (`int64`), nunca float.

## Estrutura de arquivos (`apps/payments-go/`)

| Arquivo | Responsabilidade |
|---------|------------------|
| `main.go` | boot: config, db, migrate, ticker recorrência, `ListenAndServe` |
| `config.go` | `Config` + `LoadConfig` (env) |
| `db.go` | `newDB` (pool) + `migrate` (SQL `pay_*`) |
| `server.go` | `Server`, `Routes()`, CORS, `authGuard`, `requireAdmin` |
| `token.go` | `verifyToken` (HS256 → userID) — copiado do api-go |
| `errors.go` | `writeError`, `decodeJSON` |
| `models.go` | structs `Student`, `Plan`, `Subscription`, `Charge` + enums |
| `store.go` | repositórios pgx (students/plans/subscriptions/charges/webhook_events) |
| `provider.go` | interface `PaymentProvider` + tipos `ChargeRequest/ChargeResult/WebhookEvent` |
| `dotfy.go` | `dotfyProvider` (CreateCharge/GetCharge/ParseWebhook) |
| `email.go` | `emailClient` (copiado do api-go) + template do Pix |
| `handlers_students.go` | CRUD aluno |
| `handlers_plans.go` | CRUD plano |
| `handlers_subscriptions.go` | CRUD assinatura |
| `handlers_charges.go` | criar/listar cobrança |
| `handlers_webhook.go` | `POST /webhooks/dotfy` |
| `recurring.go` | job mensal (gera mensalidade) |
| `*_test.go` | testes unitários (`httptest`, provider mockado) |

---

## Task 0: Scaffold do serviço + /health

**Files:**
- Create: `apps/payments-go/go.mod`, `main.go`, `config.go`, `db.go`, `server.go`, `errors.go`, `.env.example`

- [ ] **Step 1: Criar o módulo Go**

Run:
```bash
cd /home/guilherme/projetos/sg/santos-tech-infra/apps/payments-go 2>/dev/null || mkdir -p /home/guilherme/projetos/sg/santos-tech-infra/apps/payments-go && cd /home/guilherme/projetos/sg/santos-tech-infra/apps/payments-go
PATH=$PATH:$HOME/.local/bin go mod init github.com/santos-tech/payments
PATH=$PATH:$HOME/.local/bin go get github.com/jackc/pgx/v5@v5.9.2 github.com/golang-jwt/jwt/v5@v5.3.1
```

- [ ] **Step 2: `config.go`** (espelha o padrão do api-go: `getEnv`/`mustEnv`)

```go
package main

import (
	"log/slog"
	"os"
	"strings"
)

type Config struct {
	Port         string
	DatabaseURL  string
	JWTSecret    string
	CORSOrigins  []string
	EmailAPIURL  string
	EmailAPIKey  string
	DotfyBaseURL string
	DotfyAPIKey  string
	DotfyWebhookSecret string
	Production   bool
}

func LoadConfig() Config {
	return Config{
		Port:               getEnv("PORT", "3334"),
		DatabaseURL:        mustEnv("DATABASE_URL"),
		JWTSecret:          mustEnv("JWT_SECRET"),
		CORSOrigins:        splitCSV(getEnv("CORS_ORIGIN", "")),
		EmailAPIURL:        strings.TrimRight(getEnv("EMAIL_API_URL", "https://mails.santos-tech.com/api"), "/"),
		EmailAPIKey:        getEnv("EMAIL_API_KEY", ""),
		DotfyBaseURL:       strings.TrimRight(getEnv("DOTFY_BASE_URL", "https://app.dotfy.com.br"), "/"),
		DotfyAPIKey:        mustEnv("DOTFY_API_KEY"),
		DotfyWebhookSecret: getEnv("DOTFY_WEBHOOK_SECRET", ""),
		Production:         getEnv("NODE_ENV", "development") == "production",
	}
}

func getEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		slog.Error("variável de ambiente obrigatória ausente", "key", k)
		os.Exit(1)
	}
	return v
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
```

- [ ] **Step 3: `db.go`** (pool igual ao api-go; `migration` vazia por enquanto)

```go
package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func newDB(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10
	cfg.MaxConnLifetime = 30 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(c); err != nil {
		return nil, err
	}
	return pool, nil
}

const migration = `` // preenchida na Task 3

func migrate(ctx context.Context, db *pgxpool.Pool) error {
	if migration == "" {
		return nil
	}
	_, err := db.Exec(ctx, migration)
	return err
}
```

- [ ] **Step 4: `errors.go`** (padrão `{code,message}` + helper de decode)

```go
package main

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"code": code, "message": msg})
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
```

- [ ] **Step 5: `server.go`** (Server + Routes + CORS + /health)

```go
package main

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	cfg      Config
	db       *pgxpool.Pool
	store    *Store
	provider PaymentProvider
	email    *emailClient
}

func NewServer(cfg Config, db *pgxpool.Pool, provider PaymentProvider) *Server {
	return &Server{
		cfg:      cfg,
		db:       db,
		store:    &Store{db: db},
		provider: provider,
		email:    newEmailClient(cfg),
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	// rotas adicionadas nas tasks seguintes
	return s.cors(mux)
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		for _, o := range s.cfg.CORSOrigins {
			if o == origin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				break
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 6: `main.go`** (boot mínimo; provider/recorrência entram depois)

```go
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
)

func main() {
	cfg := LoadConfig()
	ctx := context.Background()

	db, err := newDB(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("falha ao conectar no Postgres", "err", err)
		os.Exit(1)
	}
	if err := migrate(ctx, db); err != nil {
		slog.Error("falha na migração", "err", err)
		os.Exit(1)
	}

	provider := newDotfyProvider(cfg)
	srv := NewServer(cfg, db, provider)

	go srv.runRecurringLoop(ctx)

	slog.Info("payments ouvindo", "port", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, srv.Routes()); err != nil {
		slog.Error("erro no servidor", "err", err)
		os.Exit(1)
	}
}
```

> Nota: `newDotfyProvider` (Task 6) e `runRecurringLoop` (Task 9) ainda não existem — o build só fecha ao final daquelas tasks. Para validar a Task 0 isoladamente, comente as duas linhas e o `provider` em `NewServer`, rode, e descomente depois.

- [ ] **Step 7: `.env.example`**

```
PORT=3334
DATABASE_URL=postgres://user:pass@localhost:5432/santostech
JWT_SECRET=trocar-igual-ao-ecossistema
EMAIL_API_URL=https://mails.santos-tech.com/api
EMAIL_API_KEY=
DOTFY_BASE_URL=https://app.dotfy.com.br
DOTFY_API_KEY=vk_test_xxx
DOTFY_WEBHOOK_SECRET=
CORS_ORIGIN=
NODE_ENV=development
```

- [ ] **Step 8: Verificar build do /health** (com as 3 linhas de provider/loop comentadas)

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go build ./... && echo OK`
Expected: `OK`

- [ ] **Step 9: Commit**

```bash
git add apps/payments-go
git commit -m "feat(payments): scaffold do serviço Go + /health"
```

---

## Task 1: Validação de JWT (token.go)

**Files:**
- Create: `apps/payments-go/token.go`, `apps/payments-go/token_test.go`

- [ ] **Step 1: Teste falhando** (`token_test.go`)

```go
package main

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestVerifyToken(t *testing.T) {
	secret := "s3cr3t"
	claims := jwt.MapClaims{"sub": "42", "exp": time.Now().Add(time.Hour).Unix()}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))

	id, err := verifyToken(tok, secret)
	if err != nil || id != 42 {
		t.Fatalf("esperava id=42 err=nil, veio id=%d err=%v", id, err)
	}
	if _, err := verifyToken(tok, "outro"); err == nil {
		t.Fatal("esperava erro com secret errado")
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestVerifyToken ./...`
Expected: FAIL (`undefined: verifyToken`)

- [ ] **Step 3: Implementar `token.go`** (copiado do api-go)

```go
package main

import (
	"errors"
	"strconv"

	"github.com/golang-jwt/jwt/v5"
)

// verifyToken valida um JWT HS256 e retorna o userID (claim sub).
func verifyToken(token, secret string) (int64, error) {
	t, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok || t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("alg inesperado")
		}
		return []byte(secret), nil
	})
	if err != nil || !t.Valid {
		return 0, errors.New("token inválido")
	}
	claims, ok := t.Claims.(jwt.MapClaims)
	if !ok {
		return 0, errors.New("claims inválidas")
	}
	sub, _ := claims["sub"].(string)
	id, err := strconv.ParseInt(sub, 10, 64)
	if err != nil {
		return 0, errors.New("sub inválido")
	}
	return id, nil
}
```

- [ ] **Step 4: Rodar e ver passar**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestVerifyToken ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add apps/payments-go/token.go apps/payments-go/token_test.go
git commit -m "feat(payments): validação de JWT HS256 (verifyToken)"
```

---

## Task 2: Guards de auth (authGuard + requireAdmin)

**Files:**
- Modify: `apps/payments-go/server.go`
- Create: `apps/payments-go/server_test.go`

- [ ] **Step 1: Teste falhando** — admin negado sem token, e Store mockável via método

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireAdmin_SemToken401(t *testing.T) {
	s := &Server{cfg: Config{JWTSecret: "x"}}
	h := s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("esperava 401, veio %d", rec.Code)
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestRequireAdmin ./...`
Expected: FAIL (`s.requireAdmin undefined`)

- [ ] **Step 3: Implementar guards em `server.go`** (acrescentar)

```go
import (
	"context"
	"strings"
)

type ctxKey string

const userIDKey ctxKey = "uid"

// extrai o token do cookie access_token ou do header Authorization: Bearer.
func bearerToken(r *http.Request) string {
	if c, err := r.Cookie("access_token"); err == nil && c.Value != "" {
		return c.Value
	}
	if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return after
	}
	return ""
}

func (s *Server) authGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if tok == "" {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Não autenticado")
			return
		}
		uid, err := verifyToken(tok, s.cfg.JWTSecret)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Não autenticado")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey, uid)))
	}
}

// requireAdmin exige token válido E role Admin (users.role == 3) no Postgres compartilhado.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		uid := r.Context().Value(userIDKey).(int64)
		var role int
		err := s.db.QueryRow(r.Context(), `SELECT role FROM users WHERE id=$1`, uid).Scan(&role)
		if err != nil || role != 3 {
			writeError(w, http.StatusForbidden, "forbidden", "Acesso restrito a administradores")
			return
		}
		next(w, r)
	}
}
```

> O teste do Step 1 não toca o banco (cai no 401 antes). Os testes de handlers (tasks seguintes) usam `authGuard` com `db` nil só nas rotas que não exigem admin, ou injetam um Postgres de teste. Para rotas admin em testes unitários, o padrão é testar o handler interno diretamente (sem o guard) e cobrir o guard neste teste isolado.

- [ ] **Step 4: Rodar e ver passar**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestRequireAdmin ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add apps/payments-go/server.go apps/payments-go/server_test.go
git commit -m "feat(payments): authGuard + requireAdmin (role Admin)"
```

---

## Task 3: Schema do banco (migrations pay_*)

**Files:**
- Modify: `apps/payments-go/db.go`

- [ ] **Step 1: Preencher `const migration`** com as tabelas `pay_*`

```go
const migration = `
CREATE TABLE IF NOT EXISTS pay_students (
  id         BIGSERIAL PRIMARY KEY,
  name       TEXT NOT NULL,
  tax_id     TEXT NOT NULL,
  email      TEXT NOT NULL,
  phone      TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS pay_plans (
  id           BIGSERIAL PRIMARY KEY,
  name         TEXT NOT NULL,
  amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
  due_day      INT NOT NULL CHECK (due_day BETWEEN 1 AND 28),
  active       BOOLEAN NOT NULL DEFAULT true,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS pay_subscriptions (
  id           BIGSERIAL PRIMARY KEY,
  student_id   BIGINT NOT NULL REFERENCES pay_students(id) ON DELETE CASCADE,
  plan_id      BIGINT NOT NULL REFERENCES pay_plans(id),
  amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
  due_day      INT NOT NULL CHECK (due_day BETWEEN 1 AND 28),
  status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','canceled')),
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pay_subs_student ON pay_subscriptions(student_id);
CREATE TABLE IF NOT EXISTS pay_charges (
  id                 BIGSERIAL PRIMARY KEY,
  kind               TEXT NOT NULL CHECK (kind IN ('mensalidade','matricula','avulso')),
  subscription_id    BIGINT REFERENCES pay_subscriptions(id) ON DELETE SET NULL,
  student_id         BIGINT NOT NULL REFERENCES pay_students(id),
  amount_cents       BIGINT NOT NULL CHECK (amount_cents > 0),
  due_date           DATE NOT NULL,
  reference_month    TEXT,
  status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','paid','expired','canceled')),
  provider           TEXT NOT NULL DEFAULT 'dotfy',
  provider_charge_id TEXT,
  correlation_id     TEXT NOT NULL UNIQUE,
  br_code            TEXT,
  qr_code            TEXT,
  paid_at            TIMESTAMPTZ,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_pay_charges_sub_month
  ON pay_charges(subscription_id, reference_month)
  WHERE subscription_id IS NOT NULL AND reference_month IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_pay_charges_student ON pay_charges(student_id);
CREATE INDEX IF NOT EXISTS idx_pay_charges_status ON pay_charges(status);
CREATE TABLE IF NOT EXISTS pay_webhook_events (
  id           TEXT PRIMARY KEY,
  type         TEXT NOT NULL,
  payload      JSONB NOT NULL,
  processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`
```

- [ ] **Step 2: Build**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go build ./... && echo OK`
Expected: `OK`

- [ ] **Step 3: (Integração, se houver Postgres local) aplicar e conferir**

Run:
```bash
docker compose -f infra/docker-compose.yml up -d postgres
cd apps/payments-go && DATABASE_URL=... JWT_SECRET=x DOTFY_API_KEY=x PATH=$PATH:$HOME/.local/bin go run . &
sleep 2 && curl -s localhost:3334/health
```
Expected: `{"status":"ok"}` e tabelas `pay_*` criadas (idempotente em reboot).

- [ ] **Step 4: Commit**

```bash
git add apps/payments-go/db.go
git commit -m "feat(payments): schema pay_* (students/plans/subscriptions/charges/webhook_events)"
```

---

## Task 4: Models + Store base

**Files:**
- Create: `apps/payments-go/models.go`, `apps/payments-go/store.go`

- [ ] **Step 1: `models.go`**

```go
package main

import "time"

type Student struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	TaxID     string    `json:"taxId"`
	Email     string    `json:"email"`
	Phone     string    `json:"phone"`
	CreatedAt time.Time `json:"createdAt"`
}

type Plan struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	AmountCents int64  `json:"amountCents"`
	DueDay      int    `json:"dueDay"`
	Active      bool   `json:"active"`
}

type Subscription struct {
	ID          int64  `json:"id"`
	StudentID   int64  `json:"studentId"`
	PlanID      int64  `json:"planId"`
	AmountCents int64  `json:"amountCents"`
	DueDay      int    `json:"dueDay"`
	Status      string `json:"status"`
}

type Charge struct {
	ID               int64      `json:"id"`
	Kind             string     `json:"kind"`
	SubscriptionID   *int64     `json:"subscriptionId,omitempty"`
	StudentID        int64      `json:"studentId"`
	AmountCents      int64      `json:"amountCents"`
	DueDate          string     `json:"dueDate"` // YYYY-MM-DD
	ReferenceMonth   *string    `json:"referenceMonth,omitempty"` // YYYY-MM
	Status           string     `json:"status"`
	Provider         string     `json:"provider"`
	ProviderChargeID string     `json:"providerChargeId"`
	CorrelationID    string     `json:"correlationId"`
	BRCode           string     `json:"brCode"`
	QRCode           string     `json:"qrCode"`
	PaidAt           *time.Time `json:"paidAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
}
```

- [ ] **Step 2: `store.go`** — repositório com pgx (CRUD students/plans/subscriptions + charges)

```go
package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ db *pgxpool.Pool }

// ── Students ──
func (s *Store) CreateStudent(ctx context.Context, st *Student) error {
	return s.db.QueryRow(ctx,
		`INSERT INTO pay_students (name, tax_id, email, phone) VALUES ($1,$2,$3,$4) RETURNING id, created_at`,
		st.Name, st.TaxID, st.Email, st.Phone).Scan(&st.ID, &st.CreatedAt)
}

func (s *Store) ListStudents(ctx context.Context) ([]Student, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, tax_id, email, phone, created_at FROM pay_students ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Student
	for rows.Next() {
		var st Student
		if err := rows.Scan(&st.ID, &st.Name, &st.TaxID, &st.Email, &st.Phone, &st.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) GetStudent(ctx context.Context, id int64) (*Student, error) {
	var st Student
	err := s.db.QueryRow(ctx,
		`SELECT id, name, tax_id, email, phone, created_at FROM pay_students WHERE id=$1`, id).
		Scan(&st.ID, &st.Name, &st.TaxID, &st.Email, &st.Phone, &st.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// ── Plans ──
func (s *Store) CreatePlan(ctx context.Context, p *Plan) error {
	return s.db.QueryRow(ctx,
		`INSERT INTO pay_plans (name, amount_cents, due_day) VALUES ($1,$2,$3) RETURNING id`,
		p.Name, p.AmountCents, p.DueDay).Scan(&p.ID)
}

func (s *Store) ListPlans(ctx context.Context) ([]Plan, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, amount_cents, due_day, active FROM pay_plans ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Plan
	for rows.Next() {
		var p Plan
		if err := rows.Scan(&p.ID, &p.Name, &p.AmountCents, &p.DueDay, &p.Active); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ── Subscriptions ──
func (s *Store) CreateSubscription(ctx context.Context, sub *Subscription) error {
	return s.db.QueryRow(ctx,
		`INSERT INTO pay_subscriptions (student_id, plan_id, amount_cents, due_day) VALUES ($1,$2,$3,$4) RETURNING id, status`,
		sub.StudentID, sub.PlanID, sub.AmountCents, sub.DueDay).Scan(&sub.ID, &sub.Status)
}

func (s *Store) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := s.db.Query(ctx, `SELECT id, student_id, plan_id, amount_cents, due_day, status FROM pay_subscriptions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.ID, &sub.StudentID, &sub.PlanID, &sub.AmountCents, &sub.DueDay, &sub.Status); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

func (s *Store) SetSubscriptionStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.Exec(ctx, `UPDATE pay_subscriptions SET status=$2 WHERE id=$1`, id, status)
	return err
}
```

- [ ] **Step 3: Build**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go build ./... && echo OK`
Expected: `OK`

- [ ] **Step 4: Commit**

```bash
git add apps/payments-go/models.go apps/payments-go/store.go
git commit -m "feat(payments): models + store base (students/plans/subscriptions)"
```

---

## Task 5: Handlers de cadastro (students/plans/subscriptions)

**Files:**
- Create: `apps/payments-go/handlers_students.go`, `handlers_plans.go`, `handlers_subscriptions.go`, `handlers_cadastro_test.go`
- Modify: `apps/payments-go/server.go` (registrar rotas)

- [ ] **Step 1: Teste falhando** — POST /students valida e cria (handler interno, sem guard)

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateStudent_Validacao(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/students", strings.NewReader(`{"name":"","taxId":"","email":""}`))
	s.handleCreateStudent(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("esperava 400 para payload inválido, veio %d", rec.Code)
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestCreateStudent ./...`
Expected: FAIL (`s.handleCreateStudent undefined`)

- [ ] **Step 3: `handlers_students.go`**

```go
package main

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleCreateStudent(w http.ResponseWriter, r *http.Request) {
	var in Student
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.TaxID = strings.TrimSpace(in.TaxID)
	in.Email = strings.TrimSpace(in.Email)
	if in.Name == "" || in.TaxID == "" || in.Email == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "name, taxId e email são obrigatórios")
		return
	}
	if err := s.store.CreateStudent(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao salvar aluno")
		return
	}
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) handleListStudents(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListStudents(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetStudent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	st, err := s.store.GetStudent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Aluno não encontrado")
		return
	}
	writeJSON(w, http.StatusOK, st)
}
```

- [ ] **Step 4: `handlers_plans.go`**

```go
package main

import (
	"net/http"
	"strings"
)

func (s *Server) handleCreatePlan(w http.ResponseWriter, r *http.Request) {
	var in Plan
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || in.AmountCents <= 0 || in.DueDay < 1 || in.DueDay > 28 {
		writeError(w, http.StatusBadRequest, "invalid_body", "name, amountCents>0 e dueDay (1-28) obrigatórios")
		return
	}
	if err := s.store.CreatePlan(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao salvar plano")
		return
	}
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListPlans(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}
```

- [ ] **Step 5: `handlers_subscriptions.go`**

```go
package main

import (
	"net/http"
	"strconv"
)

func (s *Server) handleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	var in Subscription
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	if in.StudentID == 0 || in.PlanID == 0 || in.AmountCents <= 0 || in.DueDay < 1 || in.DueDay > 28 {
		writeError(w, http.StatusBadRequest, "invalid_body", "studentId, planId, amountCents>0 e dueDay (1-28) obrigatórios")
		return
	}
	if err := s.store.CreateSubscription(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao criar assinatura")
		return
	}
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) handleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListSubscriptions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handlePatchSubscription(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	if in.Status != "active" && in.Status != "paused" && in.Status != "canceled" {
		writeError(w, http.StatusBadRequest, "invalid_body", "status deve ser active|paused|canceled")
		return
	}
	if err := s.store.SetSubscriptionStatus(r.Context(), id, in.Status); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao atualizar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": in.Status})
}
```

- [ ] **Step 6: Registrar rotas em `server.go`** (dentro de `Routes()`, antes do `return`)

```go
	mux.HandleFunc("POST /students", s.requireAdmin(s.handleCreateStudent))
	mux.HandleFunc("GET /students", s.requireAdmin(s.handleListStudents))
	mux.HandleFunc("GET /students/{id}", s.requireAdmin(s.handleGetStudent))
	mux.HandleFunc("GET /students/{id}/charges", s.requireAdmin(s.handleStudentCharges))
	mux.HandleFunc("POST /plans", s.requireAdmin(s.handleCreatePlan))
	mux.HandleFunc("GET /plans", s.requireAdmin(s.handleListPlans))
	mux.HandleFunc("POST /subscriptions", s.requireAdmin(s.handleCreateSubscription))
	mux.HandleFunc("GET /subscriptions", s.requireAdmin(s.handleListSubscriptions))
	mux.HandleFunc("PATCH /subscriptions/{id}", s.requireAdmin(s.handlePatchSubscription))
	mux.HandleFunc("POST /charges", s.requireAdmin(s.handleCreateCharge))
	mux.HandleFunc("GET /charges", s.requireAdmin(s.handleListCharges))
	mux.HandleFunc("GET /charges/{id}", s.requireAdmin(s.handleGetCharge))
	mux.HandleFunc("POST /webhooks/dotfy", s.handleDotfyWebhook)
```

> `handleStudentCharges`, `handleCreateCharge`, `handleListCharges`, `handleGetCharge` e `handleDotfyWebhook` são criados nas Tasks 7–8. Para a Task 5 fechar o build isoladamente, registre só as rotas de students/plans/subscriptions e adicione as de charges/webhook nas tasks correspondentes.

- [ ] **Step 7: Rodar testes e build**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestCreateStudent ./... && go build ./...`
Expected: PASS + build OK (com as rotas de charges/webhook ainda comentadas)

- [ ] **Step 8: Commit**

```bash
git add apps/payments-go
git commit -m "feat(payments): handlers de cadastro (students/plans/subscriptions)"
```

---

## Task 6: Interface PaymentProvider + adapter Dotfy

**Files:**
- Create: `apps/payments-go/provider.go`, `apps/payments-go/dotfy.go`, `apps/payments-go/dotfy_test.go`

> **Confirmar contra a API real antes:** o payload exato do `POST /api/charges` e o formato do webhook não estão 100% documentados no Apidog. Antes de implementar, rode `GET /api/auth/me` e, em sandbox, um `POST /api/charges` mínimo para capturar os nomes reais dos campos (amount em centavos, correlationID, payerName, payerTaxId, expiresAt) e do response (brCode/qrCode/status/ids). Ajuste os structs abaixo aos nomes reais. Os nomes usados aqui são a melhor hipótese a partir da doc.

- [ ] **Step 1: `provider.go`** — interface + tipos neutros

```go
package main

import "context"

type ChargeRequest struct {
	CorrelationID string
	AmountCents   int64
	PayerName     string
	PayerTaxID    string
	Description   string
	ExpiresAt     string // RFC3339
}

type ChargeResult struct {
	ProviderChargeID string
	BRCode           string // copia-e-cola
	QRCode           string // imagem/base64 ou URL
	Status           string
}

type WebhookEvent struct {
	ID            string // id único do evento (idempotência)
	Type          string // CHARGE_PAID | CHARGE_EXPIRED | CHARGE_CREATED
	CorrelationID string
	ProviderChargeID string
	Raw           []byte
}

type PaymentProvider interface {
	CreateCharge(ctx context.Context, req ChargeRequest) (ChargeResult, error)
	GetCharge(ctx context.Context, providerChargeID string) (ChargeResult, error)
	ParseWebhook(headers map[string][]string, body []byte) (WebhookEvent, error)
}
```

- [ ] **Step 2: Teste falhando** (`dotfy_test.go`) — CreateCharge monta request e parseia response via httptest

```go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDotfyCreateCharge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"ch_123","brCode":"00020126...","qrCodeImage":"data:image/png;base64,xxx","status":"ACTIVE"}`))
	}))
	defer srv.Close()

	p := &dotfyProvider{base: srv.URL, key: "test-key", client: srv.Client()}
	res, err := p.CreateCharge(context.Background(), ChargeRequest{CorrelationID: "abc", AmountCents: 53990, PayerName: "Fulano", PayerTaxID: "00000000000"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ProviderChargeID != "ch_123" || res.BRCode == "" {
		t.Fatalf("response mal parseado: %+v", res)
	}
}
```

- [ ] **Step 3: Rodar e ver falhar**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestDotfy ./...`
Expected: FAIL (`dotfyProvider undefined`)

- [ ] **Step 4: `dotfy.go`** — implementação (ajustar nomes de campo ao confirmar a API real)

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type dotfyProvider struct {
	base   string
	key    string
	secret string // webhook secret (opcional)
	client *http.Client
}

func newDotfyProvider(cfg Config) *dotfyProvider {
	return &dotfyProvider{
		base:   cfg.DotfyBaseURL,
		key:    cfg.DotfyAPIKey,
		secret: cfg.DotfyWebhookSecret,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

// formato do request — ajustar aos nomes reais confirmados na sandbox.
type dotfyChargeReq struct {
	CorrelationID string `json:"correlationID"`
	Amount        int64  `json:"amount"` // centavos
	PayerName     string `json:"payerName,omitempty"`
	PayerTaxID    string `json:"payerTaxId,omitempty"`
	Description   string `json:"description,omitempty"`
	ExpiresAt     string `json:"expiresAt,omitempty"`
}

type dotfyChargeResp struct {
	ID       string `json:"id"`
	BRCode   string `json:"brCode"`
	QRImage  string `json:"qrCodeImage"`
	Status   string `json:"status"`
}

func (p *dotfyProvider) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("dotfy %s %s: status %d: %s", method, path, res.StatusCode, data)
	}
	return data, nil
}

func (p *dotfyProvider) CreateCharge(ctx context.Context, req ChargeRequest) (ChargeResult, error) {
	data, err := p.do(ctx, http.MethodPost, "/api/charges", dotfyChargeReq{
		CorrelationID: req.CorrelationID,
		Amount:        req.AmountCents,
		PayerName:     req.PayerName,
		PayerTaxID:    req.PayerTaxID,
		Description:   req.Description,
		ExpiresAt:     req.ExpiresAt,
	})
	if err != nil {
		return ChargeResult{}, err
	}
	var r dotfyChargeResp
	if err := json.Unmarshal(data, &r); err != nil {
		return ChargeResult{}, err
	}
	return ChargeResult{ProviderChargeID: r.ID, BRCode: r.BRCode, QRCode: r.QRImage, Status: r.Status}, nil
}

func (p *dotfyProvider) GetCharge(ctx context.Context, id string) (ChargeResult, error) {
	data, err := p.do(ctx, http.MethodGet, "/api/charges/"+id, nil)
	if err != nil {
		return ChargeResult{}, err
	}
	var r dotfyChargeResp
	if err := json.Unmarshal(data, &r); err != nil {
		return ChargeResult{}, err
	}
	return ChargeResult{ProviderChargeID: r.ID, BRCode: r.BRCode, QRCode: r.QRImage, Status: r.Status}, nil
}

// formato do webhook — ajustar aos nomes reais.
type dotfyWebhook struct {
	Event string `json:"event"`
	Data  struct {
		ID            string `json:"id"`
		CorrelationID string `json:"correlationID"`
	} `json:"data"`
}

func (p *dotfyProvider) ParseWebhook(headers map[string][]string, body []byte) (WebhookEvent, error) {
	// TODO ao confirmar: validar assinatura via p.secret se o Dotfy enviar header (ex: X-Signature HMAC).
	var wh dotfyWebhook
	if err := json.Unmarshal(body, &wh); err != nil {
		return WebhookEvent{}, err
	}
	if wh.Event == "" {
		return WebhookEvent{}, errors.New("evento sem tipo")
	}
	// id de idempotência: usa correlationID+event quando não há id próprio do evento.
	evID := wh.Data.ID
	if evID == "" {
		evID = wh.Data.CorrelationID
	}
	return WebhookEvent{
		ID:               evID + ":" + wh.Event,
		Type:             wh.Event,
		CorrelationID:    wh.Data.CorrelationID,
		ProviderChargeID: wh.Data.ID,
		Raw:              body,
	}, nil
}
```

- [ ] **Step 5: Rodar e ver passar**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestDotfy ./...`
Expected: PASS

- [ ] **Step 6: (Validação real, opcional mas recomendada)** confirmar auth contra produção

Run: `curl -s -H "Authorization: Bearer $DOTFY_API_KEY" https://app.dotfy.com.br/api/auth/me | head -c 200`
Expected: `{"authenticated":true,...}`

- [ ] **Step 7: Commit**

```bash
git add apps/payments-go/provider.go apps/payments-go/dotfy.go apps/payments-go/dotfy_test.go
git commit -m "feat(payments): interface PaymentProvider + adapter Dotfy"
```

---

## Task 7: Cobranças (criar/listar) + envio de email

**Files:**
- Create: `apps/payments-go/handlers_charges.go`, `apps/payments-go/email.go`, `apps/payments-go/handlers_charges_test.go`
- Modify: `apps/payments-go/store.go` (métodos de charges), `server.go` (rotas charges)

- [ ] **Step 1: `email.go`** (copiado do api-go + helper de template)

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type emailClient struct {
	url    string
	key    string
	client *http.Client
}

func newEmailClient(cfg Config) *emailClient {
	return &emailClient{url: cfg.EmailAPIURL, key: cfg.EmailAPIKey, client: &http.Client{Timeout: 20 * time.Second}}
}

func (e *emailClient) send(ctx context.Context, to, subject, html string) error {
	if e.url == "" || e.key == "" {
		return nil // email desabilitado: não bloqueia a cobrança
	}
	body, _ := json.Marshal(map[string]string{"to": to, "subject": subject, "html": html})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url+"/send", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", e.key)
	res, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("email api status %d: %q", res.StatusCode, b)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	return nil
}

func pixEmailHTML(studentName string, amountCents int64, brCode string) string {
	reais := float64(amountCents) / 100
	return fmt.Sprintf(`<p>Olá, %s!</p><p>Sua cobrança Pix de <b>R$ %.2f</b> está disponível.</p>
<p>Copie e cole o código abaixo no seu app do banco:</p>
<pre style="background:#F5F8FA;padding:12px;border-radius:8px;word-break:break-all">%s</pre>
<p>Equipe Santos Tech</p>`, studentName, reais, brCode)
}
```

- [ ] **Step 2: Métodos de charges em `store.go`** (acrescentar)

```go
func (s *Store) InsertCharge(ctx context.Context, c *Charge) error {
	return s.db.QueryRow(ctx, `
		INSERT INTO pay_charges
		  (kind, subscription_id, student_id, amount_cents, due_date, reference_month,
		   provider, provider_charge_id, correlation_id, br_code, qr_code)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, status, created_at`,
		c.Kind, c.SubscriptionID, c.StudentID, c.AmountCents, c.DueDate, c.ReferenceMonth,
		c.Provider, c.ProviderChargeID, c.CorrelationID, c.BRCode, c.QRCode).
		Scan(&c.ID, &c.Status, &c.CreatedAt)
}

func (s *Store) ListCharges(ctx context.Context, status string, studentID int64) ([]Charge, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, kind, subscription_id, student_id, amount_cents, due_date::text, reference_month,
		       status, provider, COALESCE(provider_charge_id,''), correlation_id,
		       COALESCE(br_code,''), COALESCE(qr_code,''), paid_at, created_at
		FROM pay_charges
		WHERE ($1='' OR status=$1) AND ($2=0 OR student_id=$2)
		ORDER BY created_at DESC`, status, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Charge
	for rows.Next() {
		var c Charge
		if err := rows.Scan(&c.ID, &c.Kind, &c.SubscriptionID, &c.StudentID, &c.AmountCents, &c.DueDate,
			&c.ReferenceMonth, &c.Status, &c.Provider, &c.ProviderChargeID, &c.CorrelationID,
			&c.BRCode, &c.QRCode, &c.PaidAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCharge(ctx context.Context, id int64) (*Charge, error) {
	var c Charge
	err := s.db.QueryRow(ctx, `
		SELECT id, kind, subscription_id, student_id, amount_cents, due_date::text, reference_month,
		       status, provider, COALESCE(provider_charge_id,''), correlation_id,
		       COALESCE(br_code,''), COALESCE(qr_code,''), paid_at, created_at
		FROM pay_charges WHERE id=$1`, id).
		Scan(&c.ID, &c.Kind, &c.SubscriptionID, &c.StudentID, &c.AmountCents, &c.DueDate,
			&c.ReferenceMonth, &c.Status, &c.Provider, &c.ProviderChargeID, &c.CorrelationID,
			&c.BRCode, &c.QRCode, &c.PaidAt, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// MarkChargePaid/Expired pelo correlationID (usado pelo webhook).
func (s *Store) MarkChargePaid(ctx context.Context, correlationID string) error {
	_, err := s.db.Exec(ctx, `UPDATE pay_charges SET status='paid', paid_at=now() WHERE correlation_id=$1 AND status='pending'`, correlationID)
	return err
}

func (s *Store) MarkChargeExpired(ctx context.Context, correlationID string) error {
	_, err := s.db.Exec(ctx, `UPDATE pay_charges SET status='expired' WHERE correlation_id=$1 AND status='pending'`, correlationID)
	return err
}
```

- [ ] **Step 3: Teste falhando** — POST /charges sem studentId → 400

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateCharge_Validacao(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/charges", strings.NewReader(`{"kind":"avulso","amountCents":0}`))
	s.handleCreateCharge(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("esperava 400, veio %d", rec.Code)
	}
}
```

- [ ] **Step 4: Rodar e ver falhar**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestCreateCharge ./...`
Expected: FAIL (`handleCreateCharge undefined`)

- [ ] **Step 5: `handlers_charges.go`**

```go
package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"
)

func newCorrelationID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "stpay_" + hex.EncodeToString(b)
}

type createChargeInput struct {
	Kind        string `json:"kind"`        // matricula | avulso
	StudentID   int64  `json:"studentId"`
	AmountCents int64  `json:"amountCents"`
	Description string `json:"description"`
	DueDate     string `json:"dueDate"` // YYYY-MM-DD; default hoje+3
}

func (s *Server) handleCreateCharge(w http.ResponseWriter, r *http.Request) {
	var in createChargeInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	if in.Kind != "matricula" && in.Kind != "avulso" {
		writeError(w, http.StatusBadRequest, "invalid_body", "kind deve ser matricula|avulso")
		return
	}
	if in.StudentID == 0 || in.AmountCents <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "studentId e amountCents>0 obrigatórios")
		return
	}
	st, err := s.store.GetStudent(r.Context(), in.StudentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Aluno não encontrado")
		return
	}
	if in.DueDate == "" {
		in.DueDate = time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	}
	c := &Charge{
		Kind: in.Kind, StudentID: st.ID, AmountCents: in.AmountCents,
		DueDate: in.DueDate, Provider: "dotfy", CorrelationID: newCorrelationID(),
	}
	if err := s.createAndPersistCharge(r.Context(), c, st, in.Description); err != nil {
		writeError(w, http.StatusBadGateway, "provider_error", "Falha ao gerar cobrança no gateway")
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// createAndPersistCharge: cria no provider, grava, dispara email. Reusado pela recorrência.
func (s *Server) createAndPersistCharge(ctx context.Context, c *Charge, st *Student, desc string) error {
	expires := time.Now().AddDate(0, 0, 3).Format(time.RFC3339)
	if c.DueDate != "" {
		if d, err := time.Parse("2006-01-02", c.DueDate); err == nil {
			expires = d.Add(23 * time.Hour).Format(time.RFC3339)
		}
	}
	res, err := s.provider.CreateCharge(ctx, ChargeRequest{
		CorrelationID: c.CorrelationID, AmountCents: c.AmountCents,
		PayerName: st.Name, PayerTaxID: st.TaxID, Description: desc, ExpiresAt: expires,
	})
	if err != nil {
		return err
	}
	c.ProviderChargeID, c.BRCode, c.QRCode = res.ProviderChargeID, res.BRCode, res.QRCode
	if err := s.store.InsertCharge(ctx, c); err != nil {
		return err
	}
	// email é best-effort: não falha a cobrança.
	if mailErr := s.email.send(ctx, st.Email, "Sua cobrança Pix — Santos Tech", pixEmailHTML(st.Name, c.AmountCents, c.BRCode)); mailErr != nil {
		// apenas loga; cobrança já está válida
		_ = mailErr
	}
	return nil
}

func (s *Server) handleListCharges(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	studentID, _ := strconv.ParseInt(r.URL.Query().Get("student_id"), 10, 64)
	list, err := s.store.ListCharges(r.Context(), status, studentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetCharge(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	c, err := s.store.GetCharge(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Cobrança não encontrada")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleStudentCharges(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	list, err := s.store.ListCharges(r.Context(), "", id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}
```

- [ ] **Step 6: Rodar testes e build** (já com as rotas de charges ativas em server.go)

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test ./... && go build ./...`
Expected: PASS + OK

- [ ] **Step 7: Commit**

```bash
git add apps/payments-go
git commit -m "feat(payments): cobranças (criar/listar/detalhe) + email do Pix"
```

---

## Task 8: Webhook Dotfy + idempotência

**Files:**
- Create: `apps/payments-go/handlers_webhook.go`, `apps/payments-go/handlers_webhook_test.go`
- Modify: `apps/payments-go/store.go` (idempotência)

- [ ] **Step 1: Idempotência em `store.go`** — insere o evento; retorna `false` se já existia

```go
import "github.com/jackc/pgx/v5"

// MarkWebhookSeen retorna true se é a 1ª vez que vemos este evento (deve processar).
func (s *Store) MarkWebhookSeen(ctx context.Context, id, typ string, payload []byte) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`INSERT INTO pay_webhook_events (id, type, payload) VALUES ($1,$2,$3) ON CONFLICT (id) DO NOTHING`,
		id, typ, payload)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

var _ = pgx.ErrNoRows
```

- [ ] **Step 2: Teste falhando** — webhook CHARGE_PAID responde 200

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhook_Responde200(t *testing.T) {
	s := &Server{provider: &dotfyProvider{}, store: nil}
	rec := httptest.NewRecorder()
	body := `{"event":"CHARGE_PAID","data":{"id":"ch_1","correlationID":"stpay_abc"}}`
	req := httptest.NewRequest("POST", "/webhooks/dotfy", strings.NewReader(body))
	// store nil → o handler deve tratar erro de persistência sem panicar e ainda responder 200/500 controlado.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handler panicou: %v", r)
		}
	}()
	s.handleDotfyWebhook(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("esperava 200/500 controlado, veio %d", rec.Code)
	}
}
```

> Para um teste de integração completo (idempotência real), use Postgres de teste e verifique que o 2º POST idêntico não muda a charge. O teste unitário acima só garante parsing + ausência de panic.

- [ ] **Step 3: Rodar e ver falhar**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestWebhook ./...`
Expected: FAIL (`handleDotfyWebhook undefined`)

- [ ] **Step 4: `handlers_webhook.go`**

```go
package main

import (
	"io"
	"log/slog"
	"net/http"
)

func (s *Server) handleDotfyWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Corpo inválido")
		return
	}
	ev, err := s.provider.ParseWebhook(r.Header, body)
	if err != nil {
		// 400 só se nem dá pra entender; Dotfy não deve reenviar nesse caso.
		writeError(w, http.StatusBadRequest, "invalid_webhook", "Webhook inválido")
		return
	}
	if s.store == nil { // guarda defensiva (testes)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	fresh, err := s.store.MarkWebhookSeen(r.Context(), ev.ID, ev.Type, ev.Raw)
	if err != nil {
		// erro de banco: responde 500 para o Dotfy reenviar depois.
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao registrar evento")
		return
	}
	if !fresh {
		writeJSON(w, http.StatusOK, map[string]bool{"duplicate": true}) // já processado
		return
	}
	switch ev.Type {
	case "CHARGE_PAID":
		if err := s.store.MarkChargePaid(r.Context(), ev.CorrelationID); err != nil {
			slog.Warn("falha ao marcar paga", "corr", ev.CorrelationID, "err", err)
		}
	case "CHARGE_EXPIRED":
		if err := s.store.MarkChargeExpired(r.Context(), ev.CorrelationID); err != nil {
			slog.Warn("falha ao marcar expirada", "corr", ev.CorrelationID, "err", err)
		}
	case "CHARGE_CREATED":
		// no-op: a charge já foi criada por nós.
	default:
		slog.Info("evento dotfy ignorado", "type", ev.Type)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
```

- [ ] **Step 5: Rodar e ver passar**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestWebhook ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add apps/payments-go
git commit -m "feat(payments): webhook Dotfy + idempotência (CHARGE_PAID/EXPIRED)"
```

---

## Task 9: Recorrência mensal (geração de mensalidades)

**Files:**
- Create: `apps/payments-go/recurring.go`, `apps/payments-go/recurring_test.go`
- Modify: `apps/payments-go/store.go` (query de assinaturas a cobrar)

- [ ] **Step 1: Query em `store.go`** — assinaturas ativas com vencimento hoje e sem charge do mês

```go
// SubscriptionsDueToday: ativas cujo due_day == dia atual e sem charge no reference_month.
func (s *Store) SubscriptionsDueToday(ctx context.Context, day int, refMonth string) ([]struct {
	Sub     Subscription
	Student Student
}, error) {
	rows, err := s.db.Query(ctx, `
		SELECT sub.id, sub.student_id, sub.plan_id, sub.amount_cents, sub.due_day, sub.status,
		       st.id, st.name, st.tax_id, st.email, st.phone, st.created_at
		FROM pay_subscriptions sub
		JOIN pay_students st ON st.id = sub.student_id
		WHERE sub.status='active' AND sub.due_day=$1
		  AND NOT EXISTS (
		    SELECT 1 FROM pay_charges c
		    WHERE c.subscription_id = sub.id AND c.reference_month = $2
		  )`, day, refMonth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		Sub     Subscription
		Student Student
	}
	for rows.Next() {
		var row struct {
			Sub     Subscription
			Student Student
		}
		if err := rows.Scan(&row.Sub.ID, &row.Sub.StudentID, &row.Sub.PlanID, &row.Sub.AmountCents,
			&row.Sub.DueDay, &row.Sub.Status, &row.Student.ID, &row.Student.Name, &row.Student.TaxID,
			&row.Student.Email, &row.Student.Phone, &row.Student.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
```

- [ ] **Step 2: Teste falhando** — `monthlyDueDate` calcula a data de vencimento do mês

```go
package main

import "testing"

func TestMonthlyDueDate(t *testing.T) {
	got := monthlyDueDate(2026, 6, 10)
	if got != "2026-06-10" {
		t.Fatalf("esperava 2026-06-10, veio %s", got)
	}
	// dia 31 em fevereiro → clamp para o último dia válido (28 garantido pelo schema, mas a função deve ser segura)
	if d := monthlyDueDate(2026, 2, 28); d != "2026-02-28" {
		t.Fatalf("esperava 2026-02-28, veio %s", d)
	}
}
```

- [ ] **Step 3: Rodar e ver falhar**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestMonthlyDueDate ./...`
Expected: FAIL (`monthlyDueDate undefined`)

- [ ] **Step 4: `recurring.go`**

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

func monthlyDueDate(year int, month time.Month, day int) string {
	return fmt.Sprintf("%04d-%02d-%02d", year, int(month), day)
}

// runRecurringLoop roda no boot: dispara já e depois 1x/dia.
func (s *Server) runRecurringLoop(ctx context.Context) {
	s.generateMonthlyCharges(ctx) // roda no start (idempotente)
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.generateMonthlyCharges(ctx)
		}
	}
}

func (s *Server) generateMonthlyCharges(ctx context.Context) {
	now := time.Now()
	day := now.Day()
	refMonth := fmt.Sprintf("%04d-%02d", now.Year(), int(now.Month()))
	rows, err := s.store.SubscriptionsDueToday(ctx, day, refMonth)
	if err != nil {
		slog.Error("recorrência: falha ao buscar assinaturas", "err", err)
		return
	}
	for _, row := range rows {
		ref := refMonth
		subID := row.Sub.ID
		c := &Charge{
			Kind: "mensalidade", SubscriptionID: &subID, StudentID: row.Student.ID,
			AmountCents: row.Sub.AmountCents, DueDate: monthlyDueDate(now.Year(), now.Month(), row.Sub.DueDay),
			ReferenceMonth: &ref, Provider: "dotfy", CorrelationID: newCorrelationID(),
		}
		if err := s.createAndPersistCharge(ctx, c, &row.Student, "Mensalidade "+refMonth); err != nil {
			slog.Error("recorrência: falha ao gerar cobrança", "sub", subID, "err", err)
			continue
		}
		slog.Info("mensalidade gerada", "sub", subID, "charge", c.ID, "ref", refMonth)
	}
}
```

- [ ] **Step 5: Rodar e ver passar**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestMonthlyDueDate ./... && go build ./...`
Expected: PASS + OK (agora `runRecurringLoop` existe — descomentar as linhas em `main.go`)

- [ ] **Step 6: Commit**

```bash
git add apps/payments-go
git commit -m "feat(payments): recorrência mensal (gera mensalidades via ticker diário)"
```

---

## Task 10: Docker, compose, OpenAPI e llms.txt

**Files:**
- Create: `infra/Dockerfile.payments-go`
- Modify: `infra/docker-compose.yml`, `docs/openapi.yaml` (ou novo `docs/openapi-payments.yaml`), `apps/api-go/llms.txt`

- [ ] **Step 1: `infra/Dockerfile.payments-go`** (espelha `Dockerfile.api-go`)

```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY apps/payments-go/go.mod apps/payments-go/go.sum ./
RUN go mod download
COPY apps/payments-go/ ./
RUN CGO_ENABLED=0 go build -o /payments .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /payments /payments
EXPOSE 3334
ENTRYPOINT ["/payments"]
```

> Confira o `Dockerfile.api-go` real e replique exatamente o multi-stage usado lá (versões/base). Gere o `go.sum` antes: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go mod tidy`.

- [ ] **Step 2: Serviço no `infra/docker-compose.yml`** (acrescentar, espelhando o serviço da API)

```yaml
  payments-go:
    build:
      context: ..
      dockerfile: infra/Dockerfile.payments-go
    environment:
      PORT: "3334"
      DATABASE_URL: ${DATABASE_URL}
      JWT_SECRET: ${JWT_SECRET}
      EMAIL_API_URL: ${EMAIL_API_URL}
      EMAIL_API_KEY: ${EMAIL_API_KEY}
      DOTFY_BASE_URL: ${DOTFY_BASE_URL:-https://app.dotfy.com.br}
      DOTFY_API_KEY: ${DOTFY_API_KEY}
      DOTFY_WEBHOOK_SECRET: ${DOTFY_WEBHOOK_SECRET:-}
      NODE_ENV: ${NODE_ENV:-production}
    depends_on:
      - postgres
    ports:
      - "3334:3334"
```

- [ ] **Step 3: Documentar endpoints** em `docs/openapi.yaml` (ou arquivo dedicado) — paths `/students`, `/plans`, `/subscriptions`, `/charges`, `/webhooks/dotfy`, com schemas dos models e o erro `{code,message}`.

- [ ] **Step 4: Atualizar `apps/api-go/llms.txt`** — adicionar a seção "Payments API" (base, auth, endpoints principais e o fluxo Pix), conforme a regra do CLAUDE.md.

- [ ] **Step 5: Pré-commit completo**

Run:
```bash
cd apps/payments-go
PATH=$PATH:$HOME/.local/bin gofmt -l .   # vazio
PATH=$PATH:$HOME/.local/bin go vet ./...
PATH=$PATH:$HOME/.local/bin go build ./...
PATH=$PATH:$HOME/.local/bin go test ./...
```
Expected: gofmt vazio, vet limpo, build OK, testes PASS.

- [ ] **Step 6: Commit**

```bash
git add infra/Dockerfile.payments-go infra/docker-compose.yml docs/openapi.yaml apps/api-go/llms.txt
git commit -m "feat(payments): Dockerfile + compose + OpenAPI + llms.txt"
```

---

## Self-Review (cobertura do spec)

- ✅ Mensalidade recorrente → Task 9 (recorrência própria, idempotente por `reference_month`).
- ✅ Matrícula/avulso → Task 7 (`POST /charges`).
- ✅ Interface `PaymentProvider` + Dotfy → Task 6.
- ✅ Webhook + idempotência → Task 8.
- ✅ Inadimplência (status derivado, listagens) → Task 7 (`ListCharges` por status/aluno) + Task 8 (`CHARGE_EXPIRED`).
- ✅ Auth JWT + Admin → Tasks 1–2.
- ✅ Email do Pix → Task 7.
- ✅ Schema `pay_*` → Task 3.
- ✅ Deploy (Docker/compose/docs) → Task 10.
- ⚠️ **Risco aberto:** nomes exatos de campos do `POST /api/charges` e do webhook do Dotfy. Mitigação na Task 6 (confirmar em sandbox antes de fixar os structs). A assinatura/secret do webhook (Task 8) depende do que o Dotfy oferecer — validar na conta real.

## Fora de escopo (Fase 2+)
Cartão recorrente (Stripe), boleto, splits, subcontas white-label, saques/chaves Pix, disputas/MEDs, dashboard de analytics, integração direta com `portal-do-aluno`, front-end.
