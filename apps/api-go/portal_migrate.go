package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgconn"
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

-- class_teacher: vínculo professor ↔ turma. A tabela class não tem dono no
-- schema legado, então até aqui não existia como escopar "as minhas turmas".
-- Criada vazia e SEM efeito enquanto PORTAL_CLASS_SCOPE estiver desligada
-- (ver portal_class_scope.go): ligar a flag antes de popular esta tabela
-- trancaria todo professor fora das próprias turmas.
CREATE TABLE IF NOT EXISTS class_teacher (
	class_id INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (class_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_class_teacher_user ON class_teacher(user_id);

-- enrollment.individual: marca a MATRÍCULA (não a turma) como aula particular
-- — uma turma de grupo pode ter um aluno particular matriculado nela junto
-- com os demais. Registrado aqui pra documentar o que já foi aplicado
-- manualmente em produção (ver portalSetStudentIndividual).
ALTER TABLE enrollment ADD COLUMN IF NOT EXISTS individual BOOLEAN NOT NULL DEFAULT false;

-- class_schedule: grade semanal recorrente da turma (dia da semana + horário)
-- — não existia NADA de horário/calendário pro Portal antes disso; "próxima
-- aula" e visão de calendário dependem desta tabela. Mais de um horário por
-- turma é permitido (ex.: terça E quinta), por isso sem UNIQUE em class_id.
CREATE TABLE IF NOT EXISTS class_schedule (
	id SERIAL PRIMARY KEY,
	class_id INTEGER NOT NULL,
	day_of_week SMALLINT NOT NULL CHECK (day_of_week BETWEEN 0 AND 6), -- 0=domingo .. 6=sábado
	start_time TIME NOT NULL,
	end_time TIME NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_class_schedule_class ON class_schedule(class_id);
`

// portalLegacyIndexes: índices sobre as tabelas do schema legado do portal
// (enrollment, answer, progress_student_phase, container_tasks, question) —
// colunas que aparecem em quase todo WHERE/JOIN deste código e que não tinham
// cobertura porque a DDL dessas tabelas não é nossa.
//
// Cada statement roda ISOLADO e é tolerante a falha, por dois motivos:
//   - a tabela pode não existir neste banco (42P01) — é DDL de terceiro;
//   - CREATE INDEX (sem CONCURRENTLY, que não pode rodar em transação) pega
//     lock de escrita na tabela. Cada um roda na própria transação com
//     lock_timeout curto, então no pior caso o boot desiste do índice em vez de
//     ficar preso atrás de uma transação longa. O índice entra no próximo boot.
var portalLegacyIndexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_enrollment_class_user ON enrollment(class_id, user_id)`,
	`CREATE INDEX IF NOT EXISTS idx_enrollment_user ON enrollment(user_id)`,
	`CREATE INDEX IF NOT EXISTS idx_answer_exercise_user ON answer(exercise_id, user_id)`,
	`CREATE INDEX IF NOT EXISTS idx_answer_user ON answer(user_id)`,
	`CREATE INDEX IF NOT EXISTS idx_psp_user_phase ON progress_student_phase(user_id, phase_id)`,
	`CREATE INDEX IF NOT EXISTS idx_psp_phase ON progress_student_phase(phase_id)`,
	`CREATE INDEX IF NOT EXISTS idx_container_tasks_phase_exercise ON container_tasks(phase_id, exercise_id)`,
	`CREATE INDEX IF NOT EXISTS idx_container_tasks_exercise ON container_tasks(exercise_id)`,
	`CREATE INDEX IF NOT EXISTS idx_question_exercise ON question(exercise_id)`,
}

func migratePortal(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, portalMigration); err != nil {
		return err
	}
	for _, stmt := range portalLegacyIndexes {
		if err := createPortalLegacyIndex(ctx, pool, stmt); err != nil {
			// Nunca derruba o boot: o índice é otimização, não pré-requisito.
			slog.Warn("portal: índice legado não criado", "err", err, "stmt", stmt)
		}
	}
	return nil
}

// createPortalLegacyIndex roda um CREATE INDEX na própria transação, com
// lock_timeout curto. Tabela inexistente (42P01) é ignorada em silêncio — o
// banco simplesmente não tem esse pedaço do schema do portal.
func createPortalLegacyIndex(ctx context.Context, pool *pgxpool.Pool, stmt string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '3s'`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, stmt); err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "42P01" { // undefined_table
			return nil
		}
		return err
	}
	return tx.Commit(ctx)
}
