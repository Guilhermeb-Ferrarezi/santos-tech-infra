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
	// Compatibilidade com PgBouncer em transaction mode: ele não suporta
	// prepared statements nomeados persistentes. Com DB_PREPARED_STATEMENTS=false
	// usamos statements anônimos (QueryExecModeExec). Antes isso era
	// incondicional aqui — o serviço pagava o custo do modo Exec mesmo em
	// conexão direta, e divergia dos outros cinco serviços Go do repo, que já
	// usam essa env.
	if os.Getenv("DB_PREPARED_STATEMENTS") == "false" {
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	}
	// Teto e reciclagem do pool, alinhados com os outros serviços. Sem
	// MaxConns o pgx usa max(4, NumCPU), e sem MaxConnLifetime/IdleTime as
	// conexões viviam para sempre — depois de um failover do Postgres o pool
	// ficava segurando conexões mortas em vez de reabrir.
	cfg.MaxConns = 5
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
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
