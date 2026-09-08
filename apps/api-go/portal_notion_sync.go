package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Aplicação do plano do Notion no Portal. Regra que vale pra tudo aqui:
// ADITIVO. O sync cria o que falta e NUNCA altera nem apaga o que já existe —
// nem turma, nem horário, nem nome. Assim uma correção feita no painel
// sobrevive à próxima sincronização, que é o risco real de manter duas fontes
// do mesmo dado (Notion e Portal).

type notionSyncResult struct {
	Itens            []notionPlanItem `json:"itens"`
	TurmasCriadas    []string         `json:"turmasCriadas"`
	TurmasExistentes []string         `json:"turmasExistentes"`
	CursosCriados    []string         `json:"cursosCriados"`
	HorariosCriados  int              `json:"horariosCriados"`
	HorariosJaTinham int              `json:"horariosJaTinham"`
	Ignorados        int              `json:"ignorados"`
	Avisos           []string         `json:"avisos"`
	DryRun           bool             `json:"dryRun"`
}

// portalNotionSync lê o Notion, monta o plano e — se dryRun for falso —
// aplica. O dry-run existe pra decisão ser tomada olhando o resultado, não a
// promessa: ele percorre exatamente o mesmo caminho, só não grava.
func (s *Server) portalNotionSync(ctx context.Context, dryRun bool) (*notionSyncResult, error) {
	if !s.notion.enabled() {
		return nil, appErr(503, "NOTION_DISABLED", "Integração com o Notion não configurada (NOTION_TOKEN ausente)")
	}
	rows, err := s.notion.fetchRows(ctx)
	if err != nil {
		return nil, fmt.Errorf("ler agenda do notion: %w", err)
	}
	itens := planNotionAgenda(rows)
	res := &notionSyncResult{Itens: itens, DryRun: dryRun}

	cursos := map[string]int64{} // nome normalizado -> course_id
	turmas := map[string]int64{} // notion_key       -> class_id
	avisosVistos := map[string]bool{}
	aviso := func(msg string) {
		if !avisosVistos[msg] {
			avisosVistos[msg] = true
			res.Avisos = append(res.Avisos, msg)
		}
	}

	for i := range itens {
		it := &itens[i]
		if it.Action == notionPlanIgnorado {
			res.Ignorados++
			continue
		}

		// ── curso ─────────────────────────────────────────────────────────
		chaveCurso := strings.ToLower(it.Curso)
		cursoID, ok := cursos[chaveCurso]
		if !ok {
			id, criado, err := s.portalFindOrCreateCourse(ctx, it.Curso, dryRun)
			if err != nil {
				return nil, fmt.Errorf("curso %q: %w", it.Curso, err)
			}
			if criado {
				res.CursosCriados = append(res.CursosCriados, it.Curso)
			}
			cursoID, cursos[chaveCurso] = id, id
		}

		// ── turma ─────────────────────────────────────────────────────────
		turmaID, ok := turmas[it.TurmaKey]
		if !ok {
			id, criada, err := s.portalFindOrCreateClassByNotionKey(ctx, it.TurmaKey, it.Turma, cursoID, dryRun)
			if err != nil {
				return nil, fmt.Errorf("turma %q: %w", it.Turma, err)
			}
			if criada {
				res.TurmasCriadas = append(res.TurmasCriadas, it.Turma)
			} else {
				res.TurmasExistentes = append(res.TurmasExistentes, it.Turma)
			}
			turmaID, turmas[it.TurmaKey] = id, id
		}

		// ── horário semanal ───────────────────────────────────────────────
		criado, err := s.portalAddScheduleFromNotion(ctx, turmaID, it, dryRun)
		if err != nil {
			return nil, fmt.Errorf("horário de %q: %w", it.Turma, err)
		}
		if criado {
			res.HorariosCriados++
		} else {
			res.HorariosJaTinham++
			it.Motivo = "horário já estava cadastrado"
		}

		// ── professor ─────────────────────────────────────────────────────
		if it.Professor != "" && turmaID > 0 {
			profID, err := s.portalFindTeacherByName(ctx, it.Professor)
			if err != nil {
				return nil, fmt.Errorf("professor %q: %w", it.Professor, err)
			}
			if profID == 0 {
				aviso(fmt.Sprintf("professor %q não existe como usuário no sistema — turmas dele ficaram sem professor atribuído", it.Professor))
			} else if !dryRun {
				if err := s.portalAddClassTeacher(ctx, turmaID, profID); err != nil {
					return nil, fmt.Errorf("vincular professor %q: %w", it.Professor, err)
				}
			}
		}
	}

	if len(rows) > 0 {
		aviso("alunos NÃO são criados: a base do Notion não tem e-mail, que é a chave de conta no auth central")
	}
	return res, nil
}

// portalFindOrCreateCourse casa por nome sem diferenciar maiúscula/minúscula —
// "Desenvolvimento de Games" no Notion e "Desenvolvimento de games" no Portal
// são o mesmo curso, e criar os dois seria duplicata silenciosa.
func (s *Server) portalFindOrCreateCourse(ctx context.Context, nome string, dryRun bool) (int64, bool, error) {
	var id int64
	err := s.portalDB.QueryRow(ctx, `SELECT id FROM course WHERE lower(name) = lower($1) LIMIT 1`, nome).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	if dryRun {
		return 0, true, nil
	}
	// portalCreateCourse já cria o "Módulo 1" junto — sem módulo, a turma não
	// pode ser criada (current_module_id é NOT NULL).
	curso, err := s.portalCreateCourse(ctx, portalCourseInput{Name: nome})
	if err != nil {
		return 0, false, err
	}
	var novoID int64
	if _, err := fmt.Sscan(curso.ID, &novoID); err != nil {
		return 0, false, fmt.Errorf("id de curso inesperado %q: %w", curso.ID, err)
	}
	return novoID, true, nil
}

func (s *Server) portalFindOrCreateClassByNotionKey(ctx context.Context, key, nome string, courseID int64, dryRun bool) (int64, bool, error) {
	var id int64
	err := s.portalDB.QueryRow(ctx, `SELECT id FROM class WHERE notion_key = $1 LIMIT 1`, key).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	if dryRun || courseID == 0 {
		return 0, true, nil
	}
	var moduloID int64
	if err := s.portalDB.QueryRow(ctx,
		`SELECT id FROM module WHERE course_id = $1 ORDER BY index_order, id LIMIT 1`, courseID).Scan(&moduloID); err != nil {
		return 0, false, fmt.Errorf("curso %d sem módulo: %w", courseID, err)
	}
	// A base do Notion é a grade da semana, não tem início/fim de turma. Um
	// semestre a partir de hoje é um palpite honesto e editável no painel —
	// e as duas colunas são NOT NULL, então precisam de algum valor.
	inicio := time.Now()
	err = s.portalDB.QueryRow(ctx,
		`INSERT INTO class (name, course_id, current_module_id, start_date, end_date, notion_key, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,NOW(),NOW()) RETURNING id`,
		nome, courseID, moduloID, inicio, inicio.AddDate(0, 6, 0), key).Scan(&id)
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// portalAddScheduleFromNotion grava o horário amarrado à página do Notion.
// ON CONFLICT DO NOTHING no notion_page_id: rodar o sync de novo não duplica
// nem sobrescreve um horário que já foi ajustado no painel.
func (s *Server) portalAddScheduleFromNotion(ctx context.Context, classID int64, it *notionPlanItem, dryRun bool) (bool, error) {
	if dryRun || classID == 0 {
		var existe bool
		err := s.portalDB.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM class_schedule WHERE notion_page_id = $1)`, it.NotionPageID).Scan(&existe)
		return !existe, err
	}
	var id int64
	err := s.portalDB.QueryRow(ctx,
		`INSERT INTO class_schedule (class_id, day_of_week, start_time, end_time, notion_page_id, created_at, updated_at)
		 VALUES ($1,$2,$3::time,$4::time,$5,NOW(),NOW())
		 ON CONFLICT (notion_page_id) WHERE notion_page_id IS NOT NULL DO NOTHING
		 RETURNING id`,
		classID, it.DayOfWeek, it.StartTime, it.EndTime, it.NotionPageID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // já existia
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Server) portalFindTeacherByName(ctx context.Context, nome string) (int64, error) {
	var id int64
	err := s.portalDB.QueryRow(ctx,
		`SELECT id FROM "user" WHERE role = 2 AND lower(name) = lower($1) LIMIT 1`, nome).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		slog.Error("falha ao buscar professor do notion", "nome", nome, "err", err)
		return 0, err
	}
	return id, nil
}
