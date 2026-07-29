package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// portalMigration cria o que falta no banco do domínio portal (só o que este
// código gerencia — o schema base do portal, ao contrário do auth, não é
// versionado aqui: as tabelas de curso/turma/exercício etc. já existem e não são
// criadas por este arquivo). portal_point é a exceção: tabela nova introduzida
// pelo ranking de pontos, então precisa da mesma migração idempotente no boot que
// o domínio auth já usa (ver migrate() em db.go).
const portalMigration = `
CREATE TABLE IF NOT EXISTS portal_point (
	id SERIAL PRIMARY KEY,
	user_id INTEGER NOT NULL,
	exercise_id INTEGER NOT NULL,
	points NUMERIC NOT NULL,
	reason TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_portal_point_user ON portal_point(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_portal_point_user_reason ON portal_point(user_id, reason);
`

func migratePortal(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, portalMigration)
	return err
}
