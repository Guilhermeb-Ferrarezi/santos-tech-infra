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

-- Sincronização com a base "Agenda de Aulas" do Notion (portal_notion_plan.go).
-- As chaves abaixo tornam o sync idempotente: ele encontra o que já criou em
-- vez de duplicar, e continua achando a turma mesmo se alguém a renomear pelo
-- painel. Só o sync escreve nelas; turma criada à mão fica com NULL e é
-- ignorada pelo sync (nunca é sobrescrita).
ALTER TABLE class ADD COLUMN IF NOT EXISTS notion_key TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_class_notion_key ON class(notion_key) WHERE notion_key IS NOT NULL;
ALTER TABLE class_schedule ADD COLUMN IF NOT EXISTS notion_page_id TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_class_schedule_notion ON class_schedule(notion_page_id) WHERE notion_page_id IS NOT NULL;

-- Chamada. class_schedule é a GRADE (toda terça 14h); class_session é a aula
-- que de fato aconteceu numa DATA (terça, 28/07, 14h). Precisa ser material e
-- não calculada na hora porque a falta é registrada contra uma aula
-- específica, e porque a grade muda com o tempo sem reescrever o passado.
CREATE TABLE IF NOT EXISTS class_session (
    id SERIAL PRIMARY KEY,
    class_id INTEGER NOT NULL,
    date DATE NOT NULL,
    start_time TIME,
    end_time TIME,
    canceled BOOLEAN NOT NULL DEFAULT false,
    note TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_class_session_unica ON class_session(class_id, date, start_time);
CREATE INDEX IF NOT EXISTS idx_class_session_class ON class_session(class_id, date);

-- attendance: uma linha por (aula, aluno). Sem linha = ainda não foi feita a
-- chamada daquele aluno — diferente de presente. Por isso não há default:
-- "não sei" e "estava presente" são coisas distintas num controle de falta.
CREATE TABLE IF NOT EXISTS attendance (
    id SERIAL PRIMARY KEY,
    session_id INTEGER NOT NULL REFERENCES class_session(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('presente','falta','justificada')),
    note TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_attendance_unica ON attendance(session_id, user_id);
CREATE INDEX IF NOT EXISTS idx_attendance_user ON attendance(user_id);

-- enrollment.contracted_lessons: pacote de aulas contratado pelo aluno (relevante
-- sobretudo pra matrícula particular — enrollment.individual=true). NULL = não
-- preenchido, turma de grupo normalmente fica assim; o front usa TotalPhases (o
-- currículo do curso) como denominador do progresso nesse caso.
ALTER TABLE enrollment ADD COLUMN IF NOT EXISTS contracted_lessons INTEGER;

-- class_session.teacher_id: override do professor pra UMA aula específica —
-- nulo (padrão) usa o professor fixo da turma (class_teacher), como sempre foi.
-- Existe pra cobrir reposição/troca pontual (ex.: professor da aula 8 diferente
-- do professor fixo da turma), não pra ser preenchido em toda aula.
ALTER TABLE class_session ADD COLUMN IF NOT EXISTS teacher_id INTEGER;

-- aula_count: quantas "aulas" (unidade de 1h, convenção da escola) esse
-- horário/sessão representa. Padrão 1 preserva 100% do comportamento atual
-- (turma de grupo, 1 encontro = 1 aula, mesmo com duração != 1h). Só aula
-- particular de encontro mais longo (ex.: 2h) precisa setar 2.
ALTER TABLE class_schedule ADD COLUMN IF NOT EXISTS aula_count INTEGER NOT NULL DEFAULT 1;
ALTER TABLE class_session ADD COLUMN IF NOT EXISTS aula_count INTEGER NOT NULL DEFAULT 1;

-- enrollment.contrato_drive_file_id: id do ARQUIVO (não da pasta) do contrato
-- desse aluno na pasta "Contratos" do Drive. NULL = ainda não vinculado — nem
-- todo aluno antigo tem o contrato original digitalizado/localizado ainda.
-- Quem sobe o arquivo é o dashboard (gerador de contratos ou "Vincular
-- contrato" na tela da turma); aqui só guardamos a referência.
ALTER TABLE enrollment ADD COLUMN IF NOT EXISTS contrato_drive_file_id TEXT;

-- session_diary: o "Diário de aula" — o que o professor registra de cada aula
-- dada (resumo, arquivos e vídeo no Drive). Uma linha por class_session
-- (UNIQUE em session_id): registrar de novo SOBRESCREVE, não versiona — o
-- registro tem de caber em dois minutos, então "editar" é só salvar de novo.
-- attachments é JSONB [{driveFileId,name,mimeType,size}]: os arquivos moram
-- no Drive da diretoria (pasta "Diário de aulas"), aqui fica só a referência.
-- Cai junto com a aula (ON DELETE CASCADE), como attendance.
CREATE TABLE IF NOT EXISTS session_diary (
    id SERIAL PRIMARY KEY,
    session_id INTEGER NOT NULL UNIQUE REFERENCES class_session(id) ON DELETE CASCADE,
    author_email TEXT NOT NULL,
    author_name TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    attachments JSONB NOT NULL DEFAULT '[]',
    video_url TEXT,
    video_drive_file_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- enrollment.contracted_content: o "conteúdo das aulas" combinado no contrato
-- do aluno — até aqui esse texto só existia dentro do PDF gerado pelo
-- dashboard. Guardado na matrícula pra o professor ver, na hora de registrar o
-- diário, o que foi prometido pra aquele aluno. NULL = não preenchido.
ALTER TABLE enrollment ADD COLUMN IF NOT EXISTS contracted_content TEXT;

-- Pós-aula, fase 2: o Claude lê o diário e gera as práticas (posaula_*.go).
-- session_diary ganha o estado da geração — fica NO diário, e não numa tabela
-- de jobs, porque é a tela do diário que mostra "gerando…"/"falhou": um
-- estado por aula, sobrescrito a cada geração, sem histórico (o histórico é
-- ai_run). student_summary é o resumo AMIGÁVEL pro aluno (2ª pessoa), que é
-- diferente do summary do professor (ditado, técnico, às vezes telegráfico).
ALTER TABLE session_diary ADD COLUMN IF NOT EXISTS student_summary TEXT;
ALTER TABLE session_diary ADD COLUMN IF NOT EXISTS ai_status TEXT;
ALTER TABLE session_diary ADD COLUMN IF NOT EXISTS ai_error TEXT;
ALTER TABLE session_diary ADD COLUMN IF NOT EXISTS ai_updated_at TIMESTAMPTZ;

-- ai_run: uma linha por chamada ao Claude (geração de práticas ou correção de
-- resposta aberta), com a saída CRUA guardada — quando uma prática sai
-- estranha, dá pra ver exatamente o que o modelo devolveu em vez de adivinhar.
-- idempotency_key (sha256 de sessão + versão do diário + versão do prompt)
-- é o que impede uma task reprocessada pelo asynq de gerar duas vezes.
CREATE TABLE IF NOT EXISTS ai_run (
    id SERIAL PRIMARY KEY,
    idempotency_key TEXT UNIQUE,
    kind TEXT NOT NULL,
    session_id INTEGER,
    answer_id INTEGER,
    model TEXT,
    prompt_version TEXT,
    input_chars INTEGER,
    output_raw TEXT,
    status TEXT NOT NULL,
    error TEXT,
    duration_ms INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- posaula_task: uma prática de UM aluno sobre UMA aula. É por aluno (e não
-- por aula) de propósito: o exercício do portal é por fase do currículo e
-- vazaria a prática de um aluno pro curso inteiro. user_id é o "user".id do
-- Portal (o mesmo de enrollment). deleted_at = o professor tirou do ar (ou a
-- regeneração substituiu) — nunca se apaga de verdade, o aluno pode já ter
-- visto. Cai junto com a aula (ON DELETE CASCADE), como o diário.
CREATE TABLE IF NOT EXISTS posaula_task (
    id SERIAL PRIMARY KEY,
    session_id INTEGER NOT NULL REFERENCES class_session(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    statement TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL CHECK (kind IN ('mc','aberta')),
    options JSONB NOT NULL DEFAULT '[]',
    answer_key TEXT NOT NULL DEFAULT '',
    hint TEXT NOT NULL DEFAULT '',
    difficulty TEXT NOT NULL DEFAULT 'media',
    available_from TIMESTAMPTZ NOT NULL,
    ai_run_id INTEGER,
    notified_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_posaula_task_user ON posaula_task(user_id, available_from);
CREATE INDEX IF NOT EXISTS idx_posaula_task_session ON posaula_task(session_id);

-- posaula_answer: a resposta do aluno (uma por prática — UNIQUE em task_id;
-- responder é definitivo, sem "tentar de novo"). Múltipla escolha já nasce
-- corrigida (is_correct + feedback + corrected_at); aberta nasce com
-- is_correct NULL e corrected_at NULL até o Claude devolver o feedback.
CREATE TABLE IF NOT EXISTS posaula_answer (
    id SERIAL PRIMARY KEY,
    task_id INTEGER NOT NULL UNIQUE REFERENCES posaula_task(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL,
    answer_text TEXT,
    selected_option INTEGER,
    is_correct BOOLEAN,
    feedback TEXT,
    ai_run_id INTEGER,
    answered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    corrected_at TIMESTAMPTZ
);
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
