package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ── Respostas (answer) ───────────────────────────────────────────────────────

const portalAnswerSelect = `a.id::text, u.id::text, COALESCE(u.name,''), COALESCE(u.email,''),
	a.question_id::text, COALESCE(q.statement,''),
	a.answer_text, a.selected_option::text, a.is_correct, a.feedback, a.answered_at,
	COALESCE((SELECT json_agg(json_build_object('id', qo.id::text, 'text', COALESCE(qo.option_text,''), 'isCorrect', COALESCE(qo.is_correct,false)) ORDER BY qo.id)
		FROM question_option qo WHERE qo.question_id = q.id), '[]'::json)`

const portalAnswerFrom = ` FROM answer a JOIN "user" u ON u.id = a.user_id JOIN question q ON q.id = a.question_id `

func scanAnswer(row pgx.Row) (*portalAnswerDTO, error) {
	var dto portalAnswerDTO
	var optionsJSON []byte
	if err := row.Scan(&dto.ID, &dto.Student.ID, &dto.Student.Name, &dto.Student.Email,
		&dto.QuestionID, &dto.Question, &dto.AnswerText, &dto.SelectedOption, &dto.IsCorrect,
		&dto.Feedback, &dto.AnsweredAt, &optionsJSON); err != nil {
		return nil, err
	}
	dto.Options = []portalOptionDTO{}
	if len(optionsJSON) > 0 {
		_ = json.Unmarshal(optionsJSON, &dto.Options)
	}
	return &dto, nil
}

func portalAnswerOrder(sort string) string {
	switch sort {
	case "oldest":
		return "a.answered_at ASC NULLS LAST, a.id ASC"
	case "student":
		return "COALESCE(u.name,'') ASC, a.id ASC"
	default:
		return "a.answered_at DESC NULLS LAST, a.id DESC"
	}
}

func (s *Server) portalExerciseAnswers(ctx context.Context, exerciseID int64, f portalAnswerFilter, q, sort string, p portalPagination) ([]portalAnswerDTO, portalAnswerStats, int64, error) {
	var stats portalAnswerStats
	args := []any{exerciseID}
	where := "WHERE a.exercise_id = $1"
	switch f.Status {
	case "corrigida":
		where += " AND a.is_correct IS NOT NULL"
	case "pendente":
		where += " AND a.is_correct IS NULL"
	}
	if f.StudentID > 0 {
		args = append(args, f.StudentID)
		where += fmt.Sprintf(" AND a.user_id = $%d", len(args))
	}
	if q != "" {
		args = append(args, "%"+q+"%")
		n := len(args)
		where += fmt.Sprintf(" AND (COALESCE(u.name,'') ILIKE $%d OR COALESCE(u.email,'') ILIKE $%d OR COALESCE(q.statement,'') ILIKE $%d OR COALESCE(a.answer_text,'') ILIKE $%d)", n, n, n, n)
	}

	statsSQL := `SELECT COUNT(*),
		COUNT(*) FILTER (WHERE a.is_correct IS NOT NULL),
		COUNT(*) FILTER (WHERE a.is_correct IS NULL),
		COUNT(*) FILTER (WHERE a.is_correct IS TRUE),
		COUNT(*) FILTER (WHERE a.is_correct IS FALSE),
		COUNT(DISTINCT a.user_id)` + portalAnswerFrom + where
	if err := s.db.QueryRow(ctx, statsSQL, args...).Scan(&stats.Total, &stats.Corrigidas, &stats.Pendentes, &stats.Corretas, &stats.Incorretas, &stats.TotalAlunos); err != nil {
		return nil, stats, 0, err
	}

	args = append(args, p.Limit, p.Offset)
	listSQL := fmt.Sprintf(`SELECT %s%s%s ORDER BY %s LIMIT $%d OFFSET $%d`,
		portalAnswerSelect, portalAnswerFrom, where, portalAnswerOrder(sort), len(args)-1, len(args))
	rows, err := s.db.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, stats, 0, err
	}
	defer rows.Close()
	items := []portalAnswerDTO{}
	for rows.Next() {
		dto, err := scanAnswer(rows)
		if err != nil {
			return nil, stats, 0, err
		}
		items = append(items, *dto)
	}
	return items, stats, stats.Total, rows.Err()
}

func (s *Server) portalGetAnswer(ctx context.Context, id int64) (*portalAnswerDTO, error) {
	return scanAnswer(s.db.QueryRow(ctx, `SELECT `+portalAnswerSelect+portalAnswerFrom+` WHERE a.id = $1`, id))
}

func (s *Server) portalUpdateAnswer(ctx context.Context, id int64, patch portalAnswerPatch) (*portalAnswerDTO, error) {
	tag, err := s.db.Exec(ctx, `UPDATE answer SET
		answer_text = COALESCE($2, answer_text),
		selected_option = COALESCE($3, selected_option),
		is_correct = COALESCE($4, is_correct),
		feedback = COALESCE($5, feedback)
		WHERE id = $1`, id, patch.AnswerText, patch.SelectedOption, patch.IsCorrect, patch.Feedback)
	if err != nil {
		return nil, portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, notFoundErr("Resposta")
	}
	return s.portalGetAnswer(ctx, id)
}

// portalBatchUpdateAnswers aplica o mesmo patch a várias respostas numa
// transação; devolve os ids atualizados e os não encontrados.
func (s *Server) portalBatchUpdateAnswers(ctx context.Context, ids []int64, patch portalAnswerPatch) (updated, notFound []string, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	updated, notFound = []string{}, []string{}
	for _, id := range ids {
		var got int64
		err := tx.QueryRow(ctx, `UPDATE answer SET
			answer_text = COALESCE($2, answer_text),
			selected_option = COALESCE($3, selected_option),
			is_correct = COALESCE($4, is_correct),
			feedback = COALESCE($5, feedback)
			WHERE id = $1 RETURNING id`, id, patch.AnswerText, patch.SelectedOption, patch.IsCorrect, patch.Feedback).Scan(&got)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = append(notFound, fmt.Sprint(id))
			continue
		}
		if err != nil {
			return nil, nil, portalDBErr(err)
		}
		updated = append(updated, fmt.Sprint(got))
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return updated, notFound, nil
}

// ── Visões "quem respondeu" ──────────────────────────────────────────────────

func (s *Server) portalExerciseAnswerStudents(ctx context.Context, exerciseID int64) ([]portalAnswerStudentSummaryDTO, error) {
	rows, err := s.db.Query(ctx, `SELECT u.id::text, COALESCE(u.name,''), COALESCE(u.email,''), COUNT(*), MAX(a.answered_at)
		FROM answer a JOIN "user" u ON u.id = a.user_id
		WHERE a.exercise_id = $1
		GROUP BY u.id, u.name, u.email
		ORDER BY COALESCE(u.name,'') ASC`, exerciseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnswerStudentSummaries(rows)
}

func (s *Server) portalAnswerStudents(ctx context.Context, q string, p portalPagination) ([]portalAnswerStudentSummaryDTO, int64, error) {
	args := []any{}
	where := ""
	if q != "" {
		args = append(args, "%"+q+"%")
		where = "WHERE COALESCE(u.name,'') ILIKE $1 OR COALESCE(u.email,'') ILIKE $1"
	}
	var total int64
	if err := s.db.QueryRow(ctx, `SELECT COUNT(DISTINCT a.user_id)
		FROM answer a JOIN "user" u ON u.id = a.user_id `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT u.id::text, COALESCE(u.name,''), COALESCE(u.email,''), COUNT(*), MAX(a.answered_at)
		FROM answer a JOIN "user" u ON u.id = a.user_id %s
		GROUP BY u.id, u.name, u.email
		ORDER BY MAX(a.answered_at) DESC NULLS LAST
		LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanAnswerStudentSummaries(rows)
	return items, total, err
}

func scanAnswerStudentSummaries(rows pgx.Rows) ([]portalAnswerStudentSummaryDTO, error) {
	items := []portalAnswerStudentSummaryDTO{}
	for rows.Next() {
		var dto portalAnswerStudentSummaryDTO
		if err := rows.Scan(&dto.StudentID, &dto.Name, &dto.Email, &dto.TotalAnswers, &dto.LastAnsweredAt); err != nil {
			return nil, err
		}
		items = append(items, dto)
	}
	return items, rows.Err()
}

func (s *Server) portalStudentAnsweredExercises(ctx context.Context, studentID int64, q string, p portalPagination) ([]portalStudentExerciseDTO, int64, error) {
	args := []any{studentID}
	where := "WHERE a.user_id = $1"
	if q != "" {
		args = append(args, "%"+q+"%")
		n := len(args)
		where += fmt.Sprintf(" AND (COALESCE(e.title,'') ILIKE $%d OR COALESCE(m.name,'') ILIKE $%d OR COALESCE(ph.name,'') ILIKE $%d)", n, n, n)
	}
	from := ` FROM answer a
		LEFT JOIN exercise e ON e.id = a.exercise_id
		LEFT JOIN phase ph ON ph.id = e.phase_id
		LEFT JOIN module m ON m.id = ph.module_id `
	var total int64
	if err := s.db.QueryRow(ctx, `SELECT COUNT(DISTINCT a.exercise_id)`+from+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT a.exercise_id::text, COALESCE(MAX(e.title),''), MAX(m.name), MAX(ph.name), COUNT(*), MAX(a.answered_at)
		%s%s
		GROUP BY a.exercise_id
		ORDER BY MAX(a.answered_at) DESC NULLS LAST
		LIMIT $%d OFFSET $%d`, from, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalStudentExerciseDTO{}
	for rows.Next() {
		var dto portalStudentExerciseDTO
		if err := rows.Scan(&dto.ExerciseID, &dto.Title, &dto.Module, &dto.Theme, &dto.TotalAnswers, &dto.LastAnsweredAt); err != nil {
			return nil, 0, err
		}
		items = append(items, dto)
	}
	return items, total, rows.Err()
}

// ── Progresso ────────────────────────────────────────────────────────────────

func (s *Server) portalPhaseProgress(ctx context.Context, phaseID int64) ([]portalProgressDTO, error) {
	rows, err := s.db.Query(ctx, `SELECT u.id::text, COALESCE(u.name,''), COALESCE(u.email,''),
		psp.status, COALESCE(psp.progress, 0), psp.unlocked_at, psp.completed_at
		FROM progress_student_phase psp JOIN "user" u ON u.id = psp.user_id
		WHERE psp.phase_id = $1
		ORDER BY COALESCE(u.name,'') ASC, u.id ASC`, phaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []portalProgressDTO{}
	phaseStr := fmt.Sprint(phaseID)
	for rows.Next() {
		var dto portalProgressDTO
		var status *int
		if err := rows.Scan(&dto.StudentID, &dto.Name, &dto.Email, &status, &dto.Progress, &dto.UnlockedAt, &dto.CompletedAt); err != nil {
			return nil, err
		}
		dto.PhaseID = phaseStr
		dto.Status, dto.StatusLabel = portalProgressStatus(status, dto.UnlockedAt)
		items = append(items, dto)
	}
	return items, rows.Err()
}

// portalClassProgress devolve o progresso de cada aluno matriculado em cada fase
// do módulo atual da turma (linhas aluno×fase; sem registro de progresso conta
// como não iniciado).
func (s *Server) portalClassProgress(ctx context.Context, classID int64) ([]portalProgressDTO, error) {
	rows, err := s.db.Query(ctx, `SELECT u.id::text, COALESCE(u.name,''), COALESCE(u.email,''),
		p.id::text, COALESCE(p.name,''), psp.status, COALESCE(psp.progress, 0), psp.unlocked_at, psp.completed_at
		FROM class c
		JOIN enrollment en ON en.class_id = c.id
		JOIN "user" u ON u.id = en.user_id
		JOIN phase p ON p.module_id = c.current_module_id
		LEFT JOIN progress_student_phase psp ON psp.user_id = u.id AND psp.phase_id = p.id
		WHERE c.id = $1
		ORDER BY COALESCE(u.name,'') ASC, u.id ASC, p.index_order ASC, p.id ASC`, classID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []portalProgressDTO{}
	for rows.Next() {
		var dto portalProgressDTO
		var status *int
		var phaseName string
		if err := rows.Scan(&dto.StudentID, &dto.Name, &dto.Email, &dto.PhaseID, &phaseName, &status, &dto.Progress, &dto.UnlockedAt, &dto.CompletedAt); err != nil {
			return nil, err
		}
		dto.PhaseName = &phaseName
		dto.Status, dto.StatusLabel = portalProgressStatus(status, dto.UnlockedAt)
		items = append(items, dto)
	}
	return items, rows.Err()
}
