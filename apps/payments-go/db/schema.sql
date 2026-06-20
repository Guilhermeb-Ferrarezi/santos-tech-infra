-- Schema de pagamentos (prefixo pay_).
-- Gerado a partir de db.go/migration para uso do sqlc.

CREATE TABLE pay_students (
  id         BIGSERIAL PRIMARY KEY,
  name       TEXT NOT NULL,
  tax_id     TEXT NOT NULL,
  email      TEXT NOT NULL,
  phone      TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE pay_plans (
  id           BIGSERIAL PRIMARY KEY,
  name         TEXT NOT NULL,
  amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
  due_day      INT NOT NULL CHECK (due_day BETWEEN 1 AND 28),
  active       BOOLEAN NOT NULL DEFAULT true,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE pay_subscriptions (
  id           BIGSERIAL PRIMARY KEY,
  student_id   BIGINT NOT NULL REFERENCES pay_students(id) ON DELETE CASCADE,
  plan_id      BIGINT NOT NULL REFERENCES pay_plans(id),
  amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
  due_day      INT NOT NULL CHECK (due_day BETWEEN 1 AND 28),
  status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','canceled')),
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_pay_subs_student ON pay_subscriptions(student_id);

CREATE TABLE pay_webhook_events (
  id           TEXT PRIMARY KEY,
  type         TEXT NOT NULL,
  payload      JSONB NOT NULL,
  processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE pay_products (
  id           BIGSERIAL PRIMARY KEY,
  slug         TEXT NOT NULL UNIQUE,
  name         TEXT NOT NULL,
  description  TEXT NOT NULL DEFAULT '',
  price_cents  BIGINT NOT NULL CHECK (price_cents > 0),
  active       BOOLEAN NOT NULL DEFAULT true,
  recurring    BOOLEAN NOT NULL DEFAULT false,
  periodicity  TEXT,
  due_day      INT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE pay_customers (
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL,
  tax_id     TEXT NOT NULL DEFAULT '',
  phone      TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL DEFAULT '',
  email      TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT uq_pay_customers_user_tax UNIQUE (user_id, tax_id)
);

CREATE TABLE pay_recurrences (
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

CREATE INDEX idx_pay_recurrences_status ON pay_recurrences(status);

CREATE TABLE pay_charges (
  id                 BIGSERIAL PRIMARY KEY,
  kind               TEXT NOT NULL CHECK (kind IN ('mensalidade','matricula','avulso','recorrente')),
  subscription_id    BIGINT REFERENCES pay_subscriptions(id) ON DELETE SET NULL,
  recurrence_id      BIGINT REFERENCES pay_recurrences(id) ON DELETE SET NULL,
  student_id         BIGINT REFERENCES pay_students(id),
  customer_id        BIGINT REFERENCES pay_customers(id),
  amount_cents       BIGINT NOT NULL CHECK (amount_cents > 0),
  due_date           DATE NOT NULL,
  reference_month    TEXT,
  status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','paid','expired','canceled')),
  provider           TEXT NOT NULL DEFAULT 'efi',
  provider_charge_id TEXT,
  correlation_id     TEXT NOT NULL UNIQUE,
  public_token       TEXT,
  payer_tax_id       TEXT,
  br_code            TEXT,
  qr_code            TEXT,
  paid_at            TIMESTAMPTZ,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_pay_charges_sub_month
  ON pay_charges(subscription_id, reference_month)
  WHERE subscription_id IS NOT NULL AND reference_month IS NOT NULL;

CREATE INDEX idx_pay_charges_student ON pay_charges(student_id);
CREATE INDEX idx_pay_charges_status ON pay_charges(status);
CREATE UNIQUE INDEX uq_pay_charges_public_token ON pay_charges(public_token) WHERE public_token IS NOT NULL;

CREATE TABLE pay_charge_items (
  id          BIGSERIAL PRIMARY KEY,
  charge_id   BIGINT NOT NULL REFERENCES pay_charges(id) ON DELETE CASCADE,
  product_id  BIGINT REFERENCES pay_products(id) ON DELETE SET NULL,
  name        TEXT NOT NULL,
  price_cents BIGINT NOT NULL,
  quantity    INT NOT NULL DEFAULT 1
);

CREATE INDEX idx_pay_charge_items_charge ON pay_charge_items(charge_id);
