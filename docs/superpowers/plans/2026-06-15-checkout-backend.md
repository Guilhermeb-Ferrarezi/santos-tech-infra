# Checkout Backend (payments-go) — Plano de Implementação (Plano 1 de 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Adicionar ao `apps/payments-go` o catálogo de produtos, conta de cliente, carrinho (Redis), checkout via Pix e status em tempo real (SSE via Redis pub/sub).

**Architecture:** Estende o serviço Go da Fase 1 (já em produção). Carrinho e pub/sub do SSE no **Redis** (go-redis/v9); produtos/clientes/itens pagos no **Postgres** (tabelas `pay_*`). Rotas de cliente usam `authGuard` (JWT do ecossistema); rotas admin usam `requireAdmin`.

**Tech Stack:** Go 1.25, `net/http`, `pgx/v5`, `redis/go-redis/v9`, `golang-jwt/v5`, SSE (`text/event-stream`).

---

## Contexto do código existente (Fase 1, já implementada)

- `apps/payments-go/`: `main.go`, `config.go`, `db.go` (const `migration` idempotente), `server.go` (`Server`, `Routes()`, `authGuard`, `requireAdmin`, `cors`), `store.go`, `models.go`, `errors.go` (`writeJSON`, `writeError`, `decodeJSON`), `dotfy.go` (`dotfyProvider`, `CreateCharge` usa `value` em REAIS), `handlers_charges.go` (`createAndPersistCharge`, `newCorrelationID`), `email.go`.
- `Charge` (models.go) já tem `CorrelationID`, `ProviderChargeID`, `BRCode`, `QRCode`.
- `Store{db *pgxpool.Pool}` com métodos pgx. `migrate()` roda no boot.
- Erros: `{code,message}` PT. Valores em **centavos** (int64). Pré-commit: `gofmt -l`, `go vet`, `go build`, `go test` (com `PATH=$PATH:$HOME/.local/bin`).

## Arquivos a criar/modificar

| Arquivo | Responsabilidade |
|---------|------------------|
| `config.go` (mod) | `RedisURL` |
| `redis.go` (novo) | `newRedis`, cliente go-redis |
| `db.go` (mod) | migrations `pay_products`/`pay_customers`/`pay_charge_items` + colunas em `pay_charges` |
| `models.go` (mod) | `Product`, `Customer`, `CartItem`, `ChargeItem` |
| `store.go` (mod) | repos de products/customers/charge_items + colunas novas |
| `cart.go` (novo) | carrinho no Redis (`CartStore`) |
| `hub.go` (novo) | SSE pub/sub via Redis (`publishPaid`, `subscribeCharge`) |
| `handlers_products.go` (novo) | CRUD admin de produtos |
| `handlers_customer.go` (novo) | `/me/customer`, `/me/cart`, checkout, `/me/charges` |
| `handlers_pay.go` (novo) | `GET /pay/{token}` + SSE `GET /pay/{token}/events` |
| `handlers_webhook.go` (mod) | publicar no Redis ao marcar paga |
| `server.go` (mod) | registrar rotas novas + `authGuard` injeta userID já existe |
| `main.go` (mod) | `newRedis`, passar redis ao `Server` |

---

## Task 1: Cliente Redis

**Files:** Create `apps/payments-go/redis.go`; Modify `config.go`, `main.go`, `server.go`

- [ ] **Step 1: `config.go` — adicionar `RedisURL`**

Em `type Config struct` adicione o campo:
```go
	RedisURL string
```
Em `LoadConfig()` (dentro do struct literal), adicione:
```go
		RedisURL: mustEnv("REDIS_URL"),
```

- [ ] **Step 2: `redis.go`**

```go
package main

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

func newRedis(url string) (*redis.Client, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	c := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		return nil, err
	}
	return c, nil
}
```

- [ ] **Step 3: instalar dep + `Server`/`main` recebem o redis**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go get github.com/redis/go-redis/v9@v9.20.0`

Em `server.go`, no `type Server struct` adicione `rdb *redis.Client` e o import `"github.com/redis/go-redis/v9"`. Mude a assinatura:
```go
func NewServer(cfg Config, db *pgxpool.Pool, rdb *redis.Client, provider PaymentProvider) *Server {
	return &Server{cfg: cfg, db: db, rdb: rdb, store: &Store{db: db}, provider: provider, email: newEmailClient(cfg)}
}
```
Em `main.go`, após o `newDB`/`migrate`, antes de `NewServer`:
```go
	rdb, err := newRedis(cfg.RedisURL)
	if err != nil {
		slog.Error("falha ao conectar no Redis", "err", err)
		os.Exit(1)
	}
	provider := newDotfyProvider(cfg)
	srv := NewServer(cfg, db, rdb, provider)
```

- [ ] **Step 4: build**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go build ./... && echo OK`
Expected: `OK`

- [ ] **Step 5: commit**

```bash
git add apps/payments-go
git commit -m "feat(payments): cliente Redis (carrinho + pub/sub do SSE)"
```

---

## Task 2: Migrations e models novos

**Files:** Modify `apps/payments-go/db.go`, `models.go`

- [ ] **Step 1: `db.go` — anexar ao final da `const migration` (antes da crase de fechamento)**

```sql
CREATE TABLE IF NOT EXISTS pay_products (
  id           BIGSERIAL PRIMARY KEY,
  slug         TEXT NOT NULL UNIQUE,
  name         TEXT NOT NULL,
  description  TEXT NOT NULL DEFAULT '',
  price_cents  BIGINT NOT NULL CHECK (price_cents > 0),
  active       BOOLEAN NOT NULL DEFAULT true,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS pay_customers (
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL UNIQUE,
  tax_id     TEXT NOT NULL DEFAULT '',
  phone      TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL DEFAULT '',
  email      TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE pay_charges ADD COLUMN IF NOT EXISTS customer_id BIGINT REFERENCES pay_customers(id);
ALTER TABLE pay_charges ADD COLUMN IF NOT EXISTS public_token TEXT;
ALTER TABLE pay_charges ADD COLUMN IF NOT EXISTS payer_tax_id TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS uq_pay_charges_public_token ON pay_charges(public_token) WHERE public_token IS NOT NULL;
CREATE TABLE IF NOT EXISTS pay_charge_items (
  id          BIGSERIAL PRIMARY KEY,
  charge_id   BIGINT NOT NULL REFERENCES pay_charges(id) ON DELETE CASCADE,
  product_id  BIGINT REFERENCES pay_products(id),
  name        TEXT NOT NULL,
  price_cents BIGINT NOT NULL,
  quantity    INT NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_pay_charge_items_charge ON pay_charge_items(charge_id);
```

- [ ] **Step 2: `models.go` — adicionar structs**

```go
type Product struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceCents  int64  `json:"priceCents"`
	Active      bool   `json:"active"`
}

type Customer struct {
	ID     int64  `json:"id"`
	UserID int64  `json:"userId"`
	TaxID  string `json:"taxId"`
	Phone  string `json:"phone"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

type CartItem struct {
	ProductID int64 `json:"productId"`
	Quantity  int   `json:"quantity"`
}

type ChargeItem struct {
	ProductID  *int64 `json:"productId,omitempty"`
	Name       string `json:"name"`
	PriceCents int64  `json:"priceCents"`
	Quantity   int    `json:"quantity"`
}
```

Em `Charge` (models.go) adicione os campos:
```go
	CustomerID  *int64 `json:"customerId,omitempty"`
	PublicToken string `json:"publicToken,omitempty"`
```

- [ ] **Step 3: build**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go build ./... && echo OK`
Expected: `OK`

- [ ] **Step 4: commit**

```bash
git add apps/payments-go
git commit -m "feat(payments): schema produtos/customers/charge_items + colunas de charge"
```

---

## Task 3: Store — products, customers, charge_items

**Files:** Modify `apps/payments-go/store.go`; Create `apps/payments-go/store_products_test.go`

- [ ] **Step 1: `store.go` — métodos de Product**

```go
func (s *Store) CreateProduct(ctx context.Context, p *Product) error {
	return s.db.QueryRow(ctx,
		`INSERT INTO pay_products (slug, name, description, price_cents) VALUES ($1,$2,$3,$4) RETURNING id, active`,
		p.Slug, p.Name, p.Description, p.PriceCents).Scan(&p.ID, &p.Active)
}

func (s *Store) ListProducts(ctx context.Context) ([]Product, error) {
	rows, err := s.db.Query(ctx, `SELECT id, slug, name, description, price_cents, active FROM pay_products ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Product{}
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.PriceCents, &p.Active); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProductBySlug(ctx context.Context, slug string) (*Product, error) {
	var p Product
	err := s.db.QueryRow(ctx,
		`SELECT id, slug, name, description, price_cents, active FROM pay_products WHERE slug=$1 AND active=true`, slug).
		Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.PriceCents, &p.Active)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) GetProductByID(ctx context.Context, id int64) (*Product, error) {
	var p Product
	err := s.db.QueryRow(ctx,
		`SELECT id, slug, name, description, price_cents, active FROM pay_products WHERE id=$1`, id).
		Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.PriceCents, &p.Active)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) UpdateProduct(ctx context.Context, p *Product) error {
	_, err := s.db.Exec(ctx,
		`UPDATE pay_products SET name=$2, description=$3, price_cents=$4, active=$5 WHERE id=$1`,
		p.ID, p.Name, p.Description, p.PriceCents, p.Active)
	return err
}
```

- [ ] **Step 2: `store.go` — Customer (upsert por user_id)**

```go
func (s *Store) UpsertCustomer(ctx context.Context, userID int64, name, email string) (*Customer, error) {
	var c Customer
	err := s.db.QueryRow(ctx, `
		INSERT INTO pay_customers (user_id, name, email) VALUES ($1,$2,$3)
		ON CONFLICT (user_id) DO UPDATE SET name=EXCLUDED.name, email=EXCLUDED.email
		RETURNING id, user_id, tax_id, phone, name, email`,
		userID, name, email).Scan(&c.ID, &c.UserID, &c.TaxID, &c.Phone, &c.Name, &c.Email)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) GetCustomerByUserID(ctx context.Context, userID int64) (*Customer, error) {
	var c Customer
	err := s.db.QueryRow(ctx,
		`SELECT id, user_id, tax_id, phone, name, email FROM pay_customers WHERE user_id=$1`, userID).
		Scan(&c.ID, &c.UserID, &c.TaxID, &c.Phone, &c.Name, &c.Email)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) UpdateCustomerData(ctx context.Context, userID int64, taxID, phone string) error {
	_, err := s.db.Exec(ctx, `UPDATE pay_customers SET tax_id=$2, phone=$3 WHERE user_id=$1`, userID, taxID, phone)
	return err
}
```

- [ ] **Step 3: `store.go` — charge_items + charge por token + InsertCharge com novas colunas**

Substitua o `InsertCharge` existente para incluir as colunas novas (o método atual insere sem `customer_id`/`public_token`/`payer_tax_id`):
```go
func (s *Store) InsertCharge(ctx context.Context, c *Charge) error {
	return s.db.QueryRow(ctx, `
		INSERT INTO pay_charges
		  (kind, subscription_id, student_id, customer_id, amount_cents, due_date, reference_month,
		   provider, provider_charge_id, correlation_id, public_token, payer_tax_id, br_code, qr_code)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING id, status, created_at`,
		c.Kind, c.SubscriptionID, nullableInt64(c.StudentID), c.CustomerID, c.AmountCents, c.DueDate, c.ReferenceMonth,
		c.Provider, c.ProviderChargeID, c.CorrelationID, nullStr(c.PublicToken), nullStr(payerTaxOf(c)), c.BRCode, c.QRCode).
		Scan(&c.ID, &c.Status, &c.CreatedAt)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
func payerTaxOf(c *Charge) string { return c.payerTaxID }
```

> Nota: o `student_id` da Fase 1 era NOT NULL. Para cobranças de cliente (sem aluno), torne-o nullable: adicione em `db.go` a migração `ALTER TABLE pay_charges ALTER COLUMN student_id DROP NOT NULL;` no fim da `const migration`. Adicione o campo não-exportado `payerTaxID string` ao struct `Charge` em models.go (sem tag json) para carregar o snapshot até o insert.

```go
func (s *Store) InsertChargeItems(ctx context.Context, chargeID int64, items []ChargeItem) error {
	for _, it := range items {
		if _, err := s.db.Exec(ctx,
			`INSERT INTO pay_charge_items (charge_id, product_id, name, price_cents, quantity) VALUES ($1,$2,$3,$4,$5)`,
			chargeID, it.ProductID, it.Name, it.PriceCents, it.Quantity); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetChargeByPublicToken(ctx context.Context, token string) (*Charge, error) {
	var c Charge
	err := s.db.QueryRow(ctx, `
		SELECT id, kind, student_id, customer_id, amount_cents, due_date::text, status,
		       COALESCE(br_code,''), COALESCE(qr_code,''), correlation_id, paid_at, created_at
		FROM pay_charges WHERE public_token=$1`, token).
		Scan(&c.ID, &c.Kind, &c.StudentID, &c.CustomerID, &c.AmountCents, &c.DueDate, &c.Status,
			&c.BRCode, &c.QRCode, &c.CorrelationID, &c.PaidAt, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) ListChargesByCustomer(ctx context.Context, customerID int64) ([]Charge, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, kind, amount_cents, due_date::text, status, COALESCE(br_code,''), correlation_id, paid_at, created_at
		FROM pay_charges WHERE customer_id=$1 ORDER BY created_at DESC`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Charge{}
	for rows.Next() {
		var c Charge
		if err := rows.Scan(&c.ID, &c.Kind, &c.AmountCents, &c.DueDate, &c.Status, &c.BRCode, &c.CorrelationID, &c.PaidAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
```

Em `models.go`, adicione ao `Charge` o campo não-exportado (após os exportados):
```go
	payerTaxID string // snapshot p/ insert; não serializa
```
E ajuste o `student_id` em `StudentID` para `*int64`? NÃO — mantenha `int64` e use `nullableInt64`. Como `GetChargeByPublicToken`/`ListChargesByCustomer` leem `student_id` que pode ser NULL, mude `StudentID` para `*int64` em `models.go` e ajuste os Scans da Fase 1 que leem `student_id` para `&c.StudentID` (já é ponteiro). **Atenção:** isso afeta `ListCharges`/`GetCharge`/`SubscriptionsDueToday` — troque os `&c.StudentID`/`&row.Student.ID` que apontam para a coluna `student_id` da charge por ponteiro. Onde a Fase 1 cria mensalidade/avulso com `StudentID: st.ID`, passe `StudentID: &st.ID`.

- [ ] **Step 4: teste falhando** (`store_products_test.go`) — valida slug/preço (sem banco; testa o validador puro)

```go
package main

import "testing"

func TestProductValid(t *testing.T) {
	if productValid(Product{Slug: "", Name: "x", PriceCents: 100}) == nil {
		t.Fatal("slug vazio deveria ser inválido")
	}
	if productValid(Product{Slug: "matricula", Name: "Matrícula", PriceCents: 0}) == nil {
		t.Fatal("preço 0 deveria ser inválido")
	}
	if err := productValid(Product{Slug: "matricula", Name: "Matrícula", PriceCents: 100}); err != nil {
		t.Fatalf("produto válido recusado: %v", err)
	}
}
```

- [ ] **Step 5: implementar `productValid` em `handlers_products.go`** (arquivo criado na Task 5; por ora crie um stub mínimo em `store.go` ou novo arquivo `validate.go`)

Create `apps/payments-go/validate.go`:
```go
package main

import (
	"errors"
	"strings"
)

func productValid(p Product) error {
	if strings.TrimSpace(p.Slug) == "" {
		return errors.New("slug obrigatório")
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name obrigatório")
	}
	if p.PriceCents <= 0 {
		return errors.New("priceCents deve ser > 0")
	}
	return nil
}
```

- [ ] **Step 6: rodar teste + build**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestProductValid ./... && go build ./...`
Expected: PASS + build OK

- [ ] **Step 7: commit**

```bash
git add apps/payments-go
git commit -m "feat(payments): store de produtos/customers/charge_items + nullable student_id"
```

---

## Task 4: Carrinho no Redis

**Files:** Create `apps/payments-go/cart.go`, `apps/payments-go/cart_test.go`

- [ ] **Step 1: teste falhando** (`cart_test.go`) — usa miniredis

Run primeiro: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go get github.com/alicebob/miniredis/v2@v2.38.0`

```go
package main

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestCartAddListClear(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cs := &CartStore{rdb: rdb}
	ctx := context.Background()

	if err := cs.Add(ctx, 7, 100); err != nil {
		t.Fatal(err)
	}
	if err := cs.Add(ctx, 7, 100); err != nil { // mesmo produto soma quantidade
		t.Fatal(err)
	}
	if err := cs.Add(ctx, 7, 200); err != nil {
		t.Fatal(err)
	}
	items, err := cs.List(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("esperava 2 produtos distintos, veio %d", len(items))
	}
	if err := cs.Clear(ctx, 7); err != nil {
		t.Fatal(err)
	}
	items, _ = cs.List(ctx, 7)
	if len(items) != 0 {
		t.Fatalf("carrinho deveria estar vazio, veio %d", len(items))
	}
}
```

- [ ] **Step 2: rodar e ver falhar**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestCartAddListClear ./...`
Expected: FAIL (`CartStore undefined`)

- [ ] **Step 3: `cart.go`**

```go
package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// CartStore guarda o carrinho no Redis como um hash productID->quantity, com TTL.
type CartStore struct{ rdb *redis.Client }

const cartTTL = 7 * 24 * time.Hour

func cartKey(userID int64) string { return fmt.Sprintf("cart:%d", userID) }

func (c *CartStore) Add(ctx context.Context, userID, productID int64) error {
	k := cartKey(userID)
	if err := c.rdb.HIncrBy(ctx, k, strconv.FormatInt(productID, 10), 1).Err(); err != nil {
		return err
	}
	return c.rdb.Expire(ctx, k, cartTTL).Err()
}

func (c *CartStore) Remove(ctx context.Context, userID, productID int64) error {
	return c.rdb.HDel(ctx, cartKey(userID), strconv.FormatInt(productID, 10)).Err()
}

func (c *CartStore) List(ctx context.Context, userID int64) ([]CartItem, error) {
	m, err := c.rdb.HGetAll(ctx, cartKey(userID)).Result()
	if err != nil {
		return nil, err
	}
	items := []CartItem{}
	for pid, qty := range m {
		id, _ := strconv.ParseInt(pid, 10, 64)
		q, _ := strconv.Atoi(qty)
		if id > 0 && q > 0 {
			items = append(items, CartItem{ProductID: id, Quantity: q})
		}
	}
	return items, nil
}

func (c *CartStore) Clear(ctx context.Context, userID int64) error {
	return c.rdb.Del(ctx, cartKey(userID)).Err()
}
```

- [ ] **Step 4: rodar e ver passar**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go test -run TestCartAddListClear ./...`
Expected: PASS

- [ ] **Step 5: commit**

```bash
git add apps/payments-go
git commit -m "feat(payments): carrinho no Redis (CartStore)"
```

---

## Task 5: CRUD admin de produtos

**Files:** Create `apps/payments-go/handlers_products.go`; Modify `server.go`

- [ ] **Step 1: `handlers_products.go`**

```go
package main

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleCreateProduct(w http.ResponseWriter, r *http.Request) {
	var in Product
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.Slug = strings.TrimSpace(in.Slug)
	in.Name = strings.TrimSpace(in.Name)
	if err := productValid(in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if err := s.store.CreateProduct(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao salvar produto (slug duplicado?)")
		return
	}
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) handleListProducts(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListProducts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleUpdateProduct(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in Product
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.ID = id
	if err := productValid(in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if err := s.store.UpdateProduct(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao atualizar")
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// público — usado pela tela de pagamento antes do login
func (s *Server) handleGetProductBySlug(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProductBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Produto não encontrado")
		return
	}
	writeJSON(w, http.StatusOK, p)
}
```

- [ ] **Step 2: `server.go` — registrar rotas** (dentro de `Routes()`)

```go
	mux.HandleFunc("POST /products", s.requireAdmin(s.handleCreateProduct))
	mux.HandleFunc("GET /products", s.requireAdmin(s.handleListProducts))
	mux.HandleFunc("PUT /products/{id}", s.requireAdmin(s.handleUpdateProduct))
	mux.HandleFunc("GET /products/by-slug/{slug}", s.handleGetProductBySlug) // público
```

- [ ] **Step 3: build**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go build ./... && echo OK`
Expected: `OK`

- [ ] **Step 4: commit**

```bash
git add apps/payments-go
git commit -m "feat(payments): CRUD admin de produtos + GET público por slug"
```

---

## Task 6: SSE via Redis pub/sub

**Files:** Create `apps/payments-go/hub.go`, `apps/payments-go/handlers_pay.go`; Modify `handlers_webhook.go`, `server.go`

- [ ] **Step 1: `hub.go`** — publish/subscribe no canal por token

```go
package main

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

func chargeChannel(token string) string { return fmt.Sprintf("pay:charge:%s", token) }

// publishChargePaid avisa os streams SSE inscritos no token que a cobrança foi paga.
func (s *Server) publishChargePaid(ctx context.Context, token string) {
	if token == "" {
		return
	}
	_ = s.rdb.Publish(ctx, chargeChannel(token), "paid").Err()
}

// subscribeCharge devolve um pubsub do Redis para o token (lembre de Close no fim).
func (s *Server) subscribeCharge(ctx context.Context, token string) *redis.PubSub {
	return s.rdb.Subscribe(ctx, chargeChannel(token))
}
```

- [ ] **Step 2: `handlers_pay.go`** — DTO público + SSE

```go
package main

import (
	"fmt"
	"net/http"
)

type payDTO struct {
	AmountCents int64  `json:"amountCents"`
	BRCode      string `json:"brCode"`
	QRCode      string `json:"qrCode"`
	Status      string `json:"status"`
	DueDate     string `json:"dueDate"`
}

func (s *Server) handleGetPay(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetChargeByPublicToken(r.Context(), r.PathValue("token"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Cobrança não encontrada")
		return
	}
	writeJSON(w, http.StatusOK, payDTO{
		AmountCents: c.AmountCents, BRCode: c.BRCode, QRCode: c.QRCode, Status: c.Status, DueDate: c.DueDate,
	})
}

// SSE: envia o status atual e, quando o webhook publicar "paid", empurra e encerra.
func (s *Server) handlePayEvents(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	c, err := s.store.GetChargeByPublicToken(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Cobrança não encontrada")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "sse_unsupported", "Streaming indisponível")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// estado atual já resolve quem chega depois do pagamento
	fmt.Fprintf(w, "event: status\ndata: %s\n\n", c.Status)
	flusher.Flush()
	if c.Status != "pending" {
		return
	}

	pubsub := s.subscribeCharge(r.Context(), token)
	defer pubsub.Close()
	ch := pubsub.Channel()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			if msg != nil && msg.Payload == "paid" {
				fmt.Fprintf(w, "event: paid\ndata: paid\n\n")
				flusher.Flush()
				return
			}
		}
	}
}
```

- [ ] **Step 3: `handlers_webhook.go` — publicar ao marcar paga**

No `case "CHARGE_PAID":`, logo após `s.store.MarkChargePaid(...)` ter sucesso, publique. Substitua o bloco por:
```go
	case "CHARGE_PAID":
		if err := s.store.MarkChargePaid(r.Context(), ev.CorrelationID); err != nil {
			slog.Warn("falha ao marcar paga", "corr", ev.CorrelationID, "err", err)
		} else if tok, e := s.store.PublicTokenByCorrelation(r.Context(), ev.CorrelationID); e == nil {
			s.publishChargePaid(r.Context(), tok)
		}
```

Em `store.go` adicione:
```go
func (s *Store) PublicTokenByCorrelation(ctx context.Context, correlationID string) (string, error) {
	var tok *string
	err := s.db.QueryRow(ctx, `SELECT public_token FROM pay_charges WHERE correlation_id=$1`, correlationID).Scan(&tok)
	if err != nil || tok == nil {
		return "", err
	}
	return *tok, nil
}
```

- [ ] **Step 4: `server.go` — rotas públicas**

```go
	mux.HandleFunc("GET /pay/{token}", s.handleGetPay)
	mux.HandleFunc("GET /pay/{token}/events", s.handlePayEvents)
```

- [ ] **Step 5: build + commit**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go build ./... && echo OK`
Expected: `OK`
```bash
git add apps/payments-go
git commit -m "feat(payments): SSE de status via Redis pub/sub + DTO público da cobrança"
```

---

## Task 7: Endpoints de cliente (/me/*) + checkout

**Files:** Create `apps/payments-go/handlers_customer.go`; Modify `server.go`. Depende de `authGuard` injetar `userIDKey` (já existe na Fase 1).

- [ ] **Step 1: `handlers_customer.go`**

```go
package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) uid(r *http.Request) int64 { return r.Context().Value(userIDKey).(int64) }

// resolve (ou cria) o customer do usuário logado. name/email vêm do token? Não temos —
// usamos vazio e o cliente preenche; o nome do pagador no Pix usa o que ele informar.
func (s *Server) customer(ctx context.Context, userID int64) (*Customer, error) {
	return s.store.UpsertCustomer(ctx, userID, "", "")
}

func (s *Server) handleGetMeCustomer(w http.ResponseWriter, r *http.Request) {
	c, err := s.customer(r.Context(), s.uid(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao carregar cliente")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handlePutMeCustomer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TaxID string `json:"taxId"`
		Phone string `json:"phone"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	if _, err := s.customer(r.Context(), s.uid(r)); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha")
		return
	}
	if err := s.store.UpdateCustomerData(r.Context(), s.uid(r), strings.TrimSpace(in.TaxID), strings.TrimSpace(in.Phone)); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao salvar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleGetCart(w http.ResponseWriter, r *http.Request) {
	items, err := s.cart.List(r.Context(), s.uid(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "redis_error", "Falha no carrinho")
		return
	}
	// enriquece com dados do produto
	type cartLine struct {
		Product  Product `json:"product"`
		Quantity int     `json:"quantity"`
	}
	out := []cartLine{}
	for _, it := range items {
		p, err := s.store.GetProductByID(r.Context(), it.ProductID)
		if err == nil {
			out = append(out, cartLine{Product: *p, Quantity: it.Quantity})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAddCart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Slug string `json:"slug"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	p, err := s.store.GetProductBySlug(r.Context(), in.Slug)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Produto não encontrado")
		return
	}
	if err := s.cart.Add(r.Context(), s.uid(r), p.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "redis_error", "Falha ao adicionar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRemoveCart(w http.ResponseWriter, r *http.Request) {
	pid, _ := strconv.ParseInt(r.PathValue("productId"), 10, 64)
	if err := s.cart.Remove(r.Context(), s.uid(r), pid); err != nil {
		writeError(w, http.StatusInternalServerError, "redis_error", "Falha ao remover")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleCheckout(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TaxID string `json:"taxId"`
		Phone string `json:"phone"`
		Save  bool   `json:"save"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.TaxID = strings.TrimSpace(in.TaxID)
	if in.TaxID == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "CPF obrigatório")
		return
	}
	uid := s.uid(r)
	cust, err := s.customer(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha no cliente")
		return
	}
	items, err := s.cart.List(r.Context(), uid)
	if err != nil || len(items) == 0 {
		writeError(w, http.StatusBadRequest, "empty_cart", "Carrinho vazio")
		return
	}
	// monta itens + total a partir do catálogo (preço sempre do servidor)
	var total int64
	chargeItems := []ChargeItem{}
	for _, it := range items {
		p, err := s.store.GetProductByID(r.Context(), it.ProductID)
		if err != nil {
			continue
		}
		pid := p.ID
		chargeItems = append(chargeItems, ChargeItem{ProductID: &pid, Name: p.Name, PriceCents: p.PriceCents, Quantity: it.Quantity})
		total += p.PriceCents * int64(it.Quantity)
	}
	if total <= 0 {
		writeError(w, http.StatusBadRequest, "empty_cart", "Carrinho inválido")
		return
	}
	cid := cust.ID
	c := &Charge{
		Kind: "avulso", CustomerID: &cid, AmountCents: total,
		DueDate: time.Now().AddDate(0, 0, 1).Format("2006-01-02"),
		Provider: "dotfy", CorrelationID: newCorrelationID(),
		PublicToken: newPublicToken(), payerTaxID: in.TaxID,
	}
	st := &Student{Name: "Cliente", TaxID: in.TaxID} // payerName/payerTaxId p/ o Dotfy
	if err := s.createAndPersistCharge(r.Context(), c, st, "Compra Santos Tech"); err != nil {
		writeError(w, http.StatusBadGateway, "provider_error", "Falha ao gerar cobrança")
		return
	}
	if err := s.store.InsertChargeItems(r.Context(), c.ID, chargeItems); err != nil {
		// não falha o pagamento; só loga
	}
	_ = s.cart.Clear(r.Context(), uid)
	if in.Save {
		_ = s.store.UpdateCustomerData(r.Context(), uid, in.TaxID, strings.TrimSpace(in.Phone))
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": c.PublicToken, "brCode": c.BRCode, "qrCode": c.QRCode, "amountCents": total,
	})
}

func (s *Server) handleMeCharges(w http.ResponseWriter, r *http.Request) {
	cust, err := s.customer(r.Context(), s.uid(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha")
		return
	}
	list, err := s.store.ListChargesByCustomer(r.Context(), cust.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}
```

> `createAndPersistCharge` (Fase 1) recebe `(ctx, *Charge, *Student, desc)` e usa `st.Name`/`st.TaxID` como `payerName`/`payerTaxId`. Aqui passamos um `Student` efêmero só com Name/TaxID — não é persistido (a charge usa `customer_id`). Garanta que `createAndPersistCharge` não tente gravar o Student (ele não grava; só usa os campos).

- [ ] **Step 2: `newPublicToken` em `handlers_charges.go`** (ao lado de `newCorrelationID`)

```go
func newPublicToken() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
```

- [ ] **Step 3: `server.go` — `Server` ganha `cart *CartStore` e rotas**

No `type Server struct` adicione `cart *CartStore`; em `NewServer` adicione `cart: &CartStore{rdb: rdb}`. Rotas em `Routes()`:
```go
	mux.HandleFunc("GET /me/customer", s.authGuard(s.handleGetMeCustomer))
	mux.HandleFunc("PUT /me/customer", s.authGuard(s.handlePutMeCustomer))
	mux.HandleFunc("GET /me/cart", s.authGuard(s.handleGetCart))
	mux.HandleFunc("POST /me/cart", s.authGuard(s.handleAddCart))
	mux.HandleFunc("DELETE /me/cart/{productId}", s.authGuard(s.handleRemoveCart))
	mux.HandleFunc("POST /me/cart/checkout", s.authGuard(s.handleCheckout))
	mux.HandleFunc("GET /me/charges", s.authGuard(s.handleMeCharges))
```

- [ ] **Step 4: build + teste**

Run: `cd apps/payments-go && PATH=$PATH:$HOME/.local/bin go build ./... && go test ./...`
Expected: build OK + testes PASS

- [ ] **Step 5: commit**

```bash
git add apps/payments-go
git commit -m "feat(payments): endpoints de cliente (/me/customer, carrinho, checkout, histórico)"
```

---

## Task 8: CORS com credenciais + env + docs

**Files:** Modify `server.go` (cors), `.env.example`, `infra/docker-compose.yml` (REDIS_URL), `docs/openapi-payments.yaml`, `apps/api-go/llms.txt`

- [ ] **Step 1: `server.go` — o `cors` já ecoa o Origin permitido e seta `Allow-Credentials: true`.** Garanta que `pagar.santos-tech.com` entre via `CORS_ORIGIN`. Nenhuma mudança de código se `CORS_ORIGIN` for configurável (é). Confirme que o header `Access-Control-Allow-Credentials: true` está presente (está, na Fase 1).

- [ ] **Step 2: `.env.example` — adicionar**

```
REDIS_URL=redis://default:senha@localhost:6379
```

- [ ] **Step 3: `infra/docker-compose.yml`** — no serviço `payments-go`, o `<<: *db-env` já injeta `REDIS_URL` (a âncora define DATABASE_URL e REDIS_URL). Confirme que `*db-env` inclui `REDIS_URL` (inclui). Nenhuma mudança se já herda.

- [ ] **Step 4: `docs/openapi-payments.yaml`** — adicionar os paths `/products`, `/products/by-slug/{slug}`, `/me/customer`, `/me/cart`, `/me/cart/checkout`, `/me/charges`, `/pay/{token}`, `/pay/{token}/events` com os schemas `Product`, `Customer`, `CartItem`. Seguir o estilo do arquivo existente.

- [ ] **Step 5: `apps/api-go/llms.txt`** — na seção "Pagamentos", acrescentar os endpoints de produto/carrinho/checkout/SSE e a nota de que o carrinho fica no Redis.

- [ ] **Step 6: pré-commit completo + commit**

Run:
```bash
cd apps/payments-go
PATH=$PATH:$HOME/.local/bin gofmt -w . && gofmt -l .
PATH=$PATH:$HOME/.local/bin go vet ./... && go build ./... && go test ./...
```
Expected: gofmt vazio, vet/build OK, testes PASS
```bash
git add apps/payments-go infra/docker-compose.yml docs/openapi-payments.yaml apps/api-go/llms.txt
git commit -m "chore(payments): CORS credenciais, REDIS_URL, OpenAPI e llms.txt do checkout"
```

---

## Self-Review (cobertura do spec)
- ✅ Redis carrinho → Task 4. Redis pub/sub SSE → Task 6.
- ✅ Produtos (catálogo + slug público) → Tasks 3, 5.
- ✅ Customers + CPF/telefone + save opt-in → Tasks 3, 7 (`save` no checkout).
- ✅ Carrinho + checkout + charge_items + histórico → Tasks 4, 7.
- ✅ SSE status → Task 6.
- ✅ Auth de cliente (authGuard) em todas as rotas /me/* → Task 7.
- ✅ DTO público sem dados sensíveis → Task 6.
- ⚠️ `student_id` vira nullable (Task 3) — exige ajustar os Scans/criações da Fase 1 que usam `StudentID`. O executor deve rodar `go build` e corrigir cada referência apontada pelo compilador (de `int64` para `*int64`).
