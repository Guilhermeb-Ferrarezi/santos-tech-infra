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

func migrate(ctx context.Context, db *pgxpool.Pool) error {
	if migration == "" {
		return nil
	}
	_, err := db.Exec(ctx, migration)
	return err
}
