package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// openDB conecta ao Postgres via pgxpool e retorna o pool pronto.
// Configura MaxConns e MaxConnLifetime explicitamente para limitar o uso de
// conexões (evita estourar o limite do Postgres) e reciclar conexões periodicamente
// (evita conexões "presas" atrás de pgbouncer/proxies ou após failover).
func openDB(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.ParseConfig: %w", err)
	}
	// Compatibilidade com PgBouncer em transaction mode: ele não suporta prepared
	// statements nomeados persistentes. Com DB_PREPARED_STATEMENTS=false usamos o
	// protocolo extended com statements anônimos (QueryExecModeExec). Sem a env, o
	// comportamento padrão (prepared statements normais) é mantido — conexão direta
	// ao Postgres não é penalizada.
	if os.Getenv("DB_PREPARED_STATEMENTS") == "false" {
		poolCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	}
	poolCfg.MaxConns = 10                       // teto de conexões simultâneas
	poolCfg.MaxConnLifetime = 30 * time.Minute  // recicla conexões a cada 30 min
	poolCfg.MaxConnIdleTime = 5 * time.Minute   // fecha conexões ociosas
	poolCfg.HealthCheckPeriod = 1 * time.Minute // checagem periódica de saúde

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.NewWithConfig: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// runMigrations roda todas as migrações SQL de ./migrations/ em ordem numérica.
// Guarda checksum sha256 em schema_migrations(filename, checksum, applied_at).
// Se o checksum de uma migração já aplicada divergir, aborta (drift detectado).
func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	// Garante que a tabela de controle existe (bootstrapping).
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename    TEXT PRIMARY KEY,
			checksum    TEXT NOT NULL,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		return fmt.Errorf("criar schema_migrations: %w", err)
	}

	// Lê checksums já aplicados.
	rows, err := pool.Query(ctx, `SELECT filename, checksum FROM schema_migrations ORDER BY filename`)
	if err != nil {
		return fmt.Errorf("ler schema_migrations: %w", err)
	}
	applied := map[string]string{}
	for rows.Next() {
		var fname, cs string
		if err := rows.Scan(&fname, &cs); err != nil {
			rows.Close()
			return err
		}
		applied[fname] = cs
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Lista arquivos de migração em ordem.
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("listar migrations: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, fname := range files {
		content, err := migrationsFS.ReadFile("migrations/" + fname)
		if err != nil {
			return fmt.Errorf("ler migration %s: %w", fname, err)
		}

		sum := fmt.Sprintf("%x", sha256.Sum256(content))

		if existingSum, ok := applied[fname]; ok {
			// Já aplicada — verifica drift.
			if existingSum != sum {
				return fmt.Errorf(
					"drift detectado: migração %s já aplicada com checksum %s, mas no disco é %s",
					fname, existingSum, sum,
				)
			}
			slog.Debug("migração já aplicada, pulando", "file", fname)
			continue
		}

		slog.Info("aplicando migração", "file", fname)

		// Migrações marcadas como no-transaction rodam sem wrapper BEGIN/COMMIT.
		noTx := strings.Contains(string(content[:min(len(content), 100)]), "migrate:no-transaction")

		if noTx {
			if _, err := pool.Exec(ctx, string(content)); err != nil {
				return fmt.Errorf("executar migration %s: %w", fname, err)
			}
			if _, err := pool.Exec(ctx,
				`INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)`,
				fname, sum,
			); err != nil {
				return fmt.Errorf("registrar migration %s: %w", fname, err)
			}
		} else {
			tx, err := pool.Begin(ctx)
			if err != nil {
				return fmt.Errorf("iniciar transação para migration %s: %w", fname, err)
			}
			if _, err := tx.Exec(ctx, string(content)); err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("executar migration %s: %w", fname, err)
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)`,
				fname, sum,
			); err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("registrar migration %s: %w", fname, err)
			}
			if err := tx.Commit(ctx); err != nil {
				return fmt.Errorf("commit migration %s: %w", fname, err)
			}
		}

		slog.Info("migração aplicada", "file", fname)
	}

	return nil
}

// withTenant retorna uma função que executa fn dentro de uma transação com
// SET LOCAL app.tenant_id = tenantID (compatibilidade com RLS existente).
// O pool direto (owner) é passado por quem chama; withTenant não guarda estado.
func withTenant(pool *pgxpool.Pool, tenantID TenantID) func(ctx context.Context, fn func(pgx.Tx) error) error {
	return func(ctx context.Context, fn func(pgx.Tx) error) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("withTenant Begin: %w", err)
		}

		// SET LOCAL aplica apenas a esta transação.
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, string(tenantID)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("withTenant SET LOCAL: %w", err)
		}

		if err := fn(tx); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}

		return tx.Commit(ctx)
	}
}

// min retorna o menor de dois inteiros (helper para slice de bytes).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
