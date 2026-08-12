package main

import (
	"context"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newDB conecta no Postgres compartilhado do ecossistema. Ao contrário dos
// outros serviços Go deste repo, o secrets-go não é dono de nenhuma tabela —
// veja o comentário no topo de main.go sobre a exceção à regra 6 (sqlc) do
// CLAUDE.md: o Postgres aqui serve só para 1 query (requireAdmin, em
// server.go), então não há migration própria para rodar.
func newDB(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	// Compatibilidade com PgBouncer em transaction mode (mesma ressalva dos
	// demais serviços Go — ver payments-go/db.go).
	if os.Getenv("DB_PREPARED_STATEMENTS") == "false" {
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	}
	cfg.MaxConns = 5
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
