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
