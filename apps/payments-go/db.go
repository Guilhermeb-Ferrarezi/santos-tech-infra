package main

import (
	"context"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newDB(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	// Compatibilidade com PgBouncer em transaction mode: ele não suporta prepared
	// statements nomeados persistentes. Com DB_PREPARED_STATEMENTS=false usamos o
	// protocolo extended com statements anônimos (QueryExecModeExec). Sem a env, o
	// comportamento padrão (prepared statements normais) é mantido — conexão direta
	// ao Postgres não é penalizada.
	if os.Getenv("DB_PREPARED_STATEMENTS") == "false" {
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
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

// migration cria apenas as tabelas próprias de pagamento (prefixo pay_). É idempotente
// (CREATE TABLE IF NOT EXISTS) e roda no boot. NÃO toca em tabelas do ecossistema (users…).
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
  provider           TEXT NOT NULL DEFAULT 'efi',
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
CREATE TABLE IF NOT EXISTS pay_products (
  id           BIGSERIAL PRIMARY KEY,
  slug         TEXT NOT NULL UNIQUE,
  name         TEXT NOT NULL,
  description  TEXT NOT NULL DEFAULT '',
  price_cents  BIGINT NOT NULL CHECK (price_cents > 0),
  active       BOOLEAN NOT NULL DEFAULT true,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Produto recorrente (assinatura, fase 2): quando recurring=true o checkout vira o
-- fluxo de PIX Automático (Jornada 3). periodicity/due_day descrevem os ciclos.
ALTER TABLE pay_products ADD COLUMN IF NOT EXISTS recurring   BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE pay_products ADD COLUMN IF NOT EXISTS periodicity TEXT;
ALTER TABLE pay_products ADD COLUMN IF NOT EXISTS due_day     INT;
-- charge_on_subscribe: true = cobra a 1ª parcela logo após a autorização (auto-débito);
-- false = a 1ª parcela cai no due_day (ciclo normal).
ALTER TABLE pay_products ADD COLUMN IF NOT EXISTS charge_on_subscribe BOOLEAN NOT NULL DEFAULT false;
-- Imagem de capa (image_url) e arquivo entregável (file_url, ex. PDF) do produto.
ALTER TABLE pay_products ADD COLUMN IF NOT EXISTS image_url TEXT;
ALTER TABLE pay_products ADD COLUMN IF NOT EXISTS file_url  TEXT;
CREATE TABLE IF NOT EXISTS pay_customers (
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL,
  tax_id     TEXT NOT NULL DEFAULT '',
  phone      TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL DEFAULT '',
  email      TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Cliente passa a ser único por (conta, CPF), não mais só por conta: a mesma conta
-- pode pagar com CPFs distintos (cada um vira um cliente) e o CPF identifica a pessoa.
ALTER TABLE pay_customers DROP CONSTRAINT IF EXISTS pay_customers_user_id_key;
CREATE UNIQUE INDEX IF NOT EXISTS uq_pay_customers_user_tax ON pay_customers(user_id, tax_id);
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
ALTER TABLE pay_charges ALTER COLUMN student_id DROP NOT NULL;
-- Permite excluir um produto sem apagar o histórico: o item da cobrança mantém o
-- snapshot (name/price) e o product_id vira NULL.
ALTER TABLE pay_charge_items DROP CONSTRAINT IF EXISTS pay_charge_items_product_id_fkey;
ALTER TABLE pay_charge_items ADD CONSTRAINT pay_charge_items_product_id_fkey
  FOREIGN KEY (product_id) REFERENCES pay_products(id) ON DELETE SET NULL;
-- PIX Automático (recorrência BACEN): contrato de recorrência + ciclos vinculados.
CREATE TABLE IF NOT EXISTS pay_recurrences (
  id              BIGSERIAL PRIMARY KEY,
  subscription_id BIGINT REFERENCES pay_subscriptions(id) ON DELETE SET NULL,
  product_id      BIGINT REFERENCES pay_products(id),
  customer_id     BIGINT REFERENCES pay_customers(id),
  payer_tax_id    TEXT NOT NULL,
  payer_name      TEXT NOT NULL,
  amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
  periodicity     TEXT NOT NULL DEFAULT 'MENSAL'
                    CHECK (periodicity IN ('SEMANAL','MENSAL','TRIMESTRAL','SEMESTRAL','ANUAL')),
  due_day         INT,
  start_date      DATE NOT NULL,
  end_date        DATE,
  journey         SMALLINT NOT NULL DEFAULT 2,
  efi_id_rec      TEXT,
  br_code         TEXT,
  qr_code         TEXT,
  status          TEXT NOT NULL DEFAULT 'pending_auth'
                    CHECK (status IN ('pending_auth','active','rejected','expired','canceled')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pay_recurrences_status ON pay_recurrences(status);
-- public_token: identificador opaco da assinatura p/ a tela de checkout acompanhar o
-- status (autorização) por SSE, sem expor o id interno.
ALTER TABLE pay_recurrences ADD COLUMN IF NOT EXISTS public_token TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS uq_pay_recurrences_public_token ON pay_recurrences(public_token) WHERE public_token IS NOT NULL;
ALTER TABLE pay_charges ADD COLUMN IF NOT EXISTS recurrence_id BIGINT REFERENCES pay_recurrences(id) ON DELETE SET NULL;
-- kind ganha 'recorrente' (ciclos de PIX Automático): recria o CHECK incluindo-o.
ALTER TABLE pay_charges DROP CONSTRAINT IF EXISTS pay_charges_kind_check;
ALTER TABLE pay_charges ADD CONSTRAINT pay_charges_kind_check
  CHECK (kind IN ('mensalidade','matricula','avulso','recorrente'));
-- Método de pagamento da cobrança (default 'pix' p/ as existentes). Boleto reusa
-- br_code (linha digitável) e provider_charge_id (charge_id do Efí); pdf_url/barcode
-- são específicos do boleto.
ALTER TABLE pay_charges ADD COLUMN IF NOT EXISTS method  TEXT NOT NULL DEFAULT 'pix'
  CHECK (method IN ('pix','boleto'));
ALTER TABLE pay_charges ADD COLUMN IF NOT EXISTS pdf_url TEXT;
ALTER TABLE pay_charges ADD COLUMN IF NOT EXISTS barcode TEXT;
-- Cartão: alarga o CHECK de method para incluir 'card'.
DO $$
BEGIN
  ALTER TABLE pay_charges DROP CONSTRAINT IF EXISTS pay_charges_method_check;
  ALTER TABLE pay_charges ADD CONSTRAINT pay_charges_method_check
    CHECK (method IN ('pix','boleto','card'));
EXCEPTION WHEN duplicate_object THEN NULL;
END$$;
-- Estorno: 'refunding' é o estado INTERMEDIÁRIO que reserva a cobrança antes de
-- chamar o gateway. Sem ele, o check-then-act do handler permitia dois estornos
-- simultâneos passarem pela mesma verificação e estornarem duas vezes.
-- 'creating' reserva a cobrança antes de existir na Efí (ver createAndPersistCharge).
DO $$
BEGIN
  ALTER TABLE pay_charges DROP CONSTRAINT IF EXISTS pay_charges_status_check;
  ALTER TABLE pay_charges ADD CONSTRAINT pay_charges_status_check
    CHECK (status IN ('creating','pending','paid','expired','canceled','refunding','refunded'));
EXCEPTION WHEN duplicate_object THEN NULL;
END$$;
-- refunded_cents acumula o total estornado. Estorno PARCIAL soma aqui e devolve a
-- cobrança para 'paid'; só quando o acumulado alcança amount_cents o status vira
-- 'refunded'. Antes um estorno de R$ 1 marcava a cobrança inteira como estornada.
ALTER TABLE pay_charges ADD COLUMN IF NOT EXISTS refunded_cents BIGINT NOT NULL DEFAULT 0;
-- Links de pagamento reutilizáveis.
CREATE TABLE IF NOT EXISTS pay_payment_links (
  id           BIGSERIAL PRIMARY KEY,
  public_token TEXT NOT NULL UNIQUE,
  amount_cents BIGINT,                     -- NULL = valor livre (a definir pelo pagador)
  product_ids  BIGINT[] NOT NULL DEFAULT '{}',
  methods      TEXT[]   NOT NULL DEFAULT '{"pix"}',
  coupons      TEXT[]   NOT NULL DEFAULT '{}',
  finish_url   TEXT NOT NULL DEFAULT '',
  return_url   TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','inactive')),
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE pay_charges ADD COLUMN IF NOT EXISTS link_id BIGINT REFERENCES pay_payment_links(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_pay_charges_link ON pay_charges(link_id) WHERE link_id IS NOT NULL;
-- Cupons de desconto: código único, tipo (fixed/percent) e valor, com controle de uso.
CREATE TABLE IF NOT EXISTS pay_coupons (
  id             BIGSERIAL PRIMARY KEY,
  code           TEXT NOT NULL UNIQUE,
  discount_type  TEXT NOT NULL CHECK (discount_type IN ('fixed','percent')),
  discount_value BIGINT NOT NULL CHECK (discount_value > 0),
  max_uses       BIGINT NOT NULL DEFAULT -1,
  used_count     BIGINT NOT NULL DEFAULT 0,
  active         BOOLEAN NOT NULL DEFAULT true,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pay_coupons_code ON pay_coupons(LOWER(code));
-- Saques (payouts): registra cada solicitação de saque do saldo Efí.
-- destination NÃO é armazenado aqui por segurança (dados bancários são sensíveis).
CREATE TABLE IF NOT EXISTS pay_withdrawals (
  id               BIGSERIAL PRIMARY KEY,
  amount_cents     BIGINT NOT NULL CHECK (amount_cents > 0),
  status           TEXT NOT NULL DEFAULT 'processing'
                     CHECK (status IN ('processing','completed','failed','disabled')),
  public_token     TEXT NOT NULL UNIQUE,
  idempotency_key  TEXT UNIQUE,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Rastreio do Pix Envio (payout real) na Efí: idEnvio (idempotência) e e2eId (rastreio BACEN).
-- O webhook pix atualiza o status do saque casando por esses identificadores.
ALTER TABLE pay_withdrawals ADD COLUMN IF NOT EXISTS efi_id_envio TEXT;
ALTER TABLE pay_withdrawals ADD COLUMN IF NOT EXISTS e2e_id       TEXT;
CREATE INDEX IF NOT EXISTS idx_pay_withdrawals_efi_id_envio ON pay_withdrawals(efi_id_envio);
CREATE INDEX IF NOT EXISTS idx_pay_withdrawals_e2e_id       ON pay_withdrawals(e2e_id);
-- Índices dos caminhos QUENTES: webhook (o pagamento chega por aqui e não pode
-- depender de seq scan), listagem do cliente e casamento das recorrências.
-- Sem eles, cada notificação da Efí varria pay_charges inteira.
CREATE INDEX IF NOT EXISTS idx_pay_charges_provider_charge_id
  ON pay_charges(provider_charge_id) WHERE provider_charge_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_pay_charges_customer   ON pay_charges(customer_id) WHERE customer_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_pay_charges_recurrence ON pay_charges(recurrence_id) WHERE recurrence_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_pay_recurrences_efi_id_rec ON pay_recurrences(efi_id_rec) WHERE efi_id_rec IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_pay_recurrences_payer_tax_id ON pay_recurrences(payer_tax_id);
CREATE INDEX IF NOT EXISTS idx_pay_recurrences_customer ON pay_recurrences(customer_id) WHERE customer_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_pay_customers_tax_id ON pay_customers(tax_id);
-- Webhook em duas fases: 'pending' = reivindicado, efeito AINDA NÃO aplicado;
-- 'done' = efeito aplicado (reentrega vira no-op). O DEFAULT é 'done' de propósito:
-- as linhas que já existem foram gravadas pelo esquema antigo, que só inseria depois
-- (na prática, junto) do processamento — reprocessá-las mandaria e-mail duplicado.
ALTER TABLE pay_webhook_events ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'done';
DO $$
BEGIN
  ALTER TABLE pay_webhook_events DROP CONSTRAINT IF EXISTS pay_webhook_events_status_check;
  ALTER TABLE pay_webhook_events ADD CONSTRAINT pay_webhook_events_status_check
    CHECK (status IN ('pending','done'));
EXCEPTION WHEN duplicate_object THEN NULL;
END$$;
`

func migrate(ctx context.Context, db *pgxpool.Pool) error {
	if migration == "" {
		return nil
	}
	_, err := db.Exec(ctx, migration)
	return err
}
