package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ── Overview ─────────────────────────────────────────────────────────────────

type portalOverview struct {
	Courses int64 `json:"courses"`
	Modules int64 `json:"modules"`
	Phases  int64 `json:"phases"`
	Classes int64 `json:"classes"`
	Rooms   int64 `json:"rooms"`
}

func (s *Server) portalOverview(ctx context.Context) (portalOverview, error) {
	var out portalOverview
	err := s.db.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM course),
		(SELECT COUNT(*) FROM module),
		(SELECT COUNT(*) FROM phase),
		(SELECT COUNT(*) FROM class),
		(SELECT COUNT(*) FROM class_rooms)`).Scan(&out.Courses, &out.Modules, &out.Phases, &out.Classes, &out.Rooms)
	return out, err
}

// ── Catálogo: leituras ───────────────────────────────────────────────────────

func (s *Server) portalListCourses(ctx context.Context, p portalPagination) ([]portalCourseDTO, int64, error) {
	args := []any{}
	where := ""
	if p.Query != "" {
		args = append(args, "%"+p.Query+"%")
		where = "WHERE COALESCE(name, '') ILIKE $1 OR COALESCE(description, '') ILIKE $1"
	}
	var total int64
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM course `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	limitPos := len(args) - 1
	offsetPos := len(args)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT id, name, description, is_paid, duration_hours, level, focus, price::text
		FROM course %s ORDER BY id ASC LIMIT $%d OFFSET $%d`, where, limitPos, offsetPos), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalCourseDTO{}
	for rows.Next() {
		var id int64
		var dto portalCourseDTO
		if err := rows.Scan(&id, &dto.Name, &dto.Description, &dto.IsPaid, &dto.DurationHours, &dto.Level, &dto.Focus, &dto.Price); err != nil {
			return nil, 0, err
		}
		dto.ID = fmt.Sprint(id)
		if strings.TrimSpace(dto.Name) == "" {
			dto.Name = "Curso " + dto.ID
		}
		items = append(items, dto)
	}
	return items, total, rows.Err()
}

func (s *Server) portalGetCourse(ctx context.Context, id int64) (*portalCourseDTO, error) {
	var dto portalCourseDTO
	err := s.db.QueryRow(ctx, `SELECT id::text, COALESCE(name, ''), description, is_paid, duration_hours, level, focus, price::text
		FROM course WHERE id=$1`, id).Scan(&dto.ID, &dto.Name, &dto.Description, &dto.IsPaid, &dto.DurationHours, &dto.Level, &dto.Focus, &dto.Price)
	if err != nil {
		return nil, err
	}
	if dto.Name == "" {
		dto.Name = "Curso " + dto.ID
	}
	return &dto, nil
}

func (s *Server) portalListModules(ctx context.Context, courseID int64, p portalPagination) ([]portalModuleDTO, int64, error) {
	args := []any{courseID}
	where := "WHERE course_id = $1"
	if p.Query != "" {
		args = append(args, "%"+p.Query+"%")
		where += fmt.Sprintf(" AND (COALESCE(name, '') ILIKE $%d OR COALESCE(description, '') ILIKE $%d)", len(args), len(args))
	}
	var total int64
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM module `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT id::text, course_id::text, COALESCE(name, ''), description, index_order
		FROM module %s ORDER BY index_order ASC, id ASC LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalModuleDTO{}
	for rows.Next() {
		var dto portalModuleDTO
		if err := rows.Scan(&dto.ID, &dto.CourseID, &dto.Name, &dto.Description, &dto.IndexOrder); err != nil {
			return nil, 0, err
		}
		if dto.Name == "" {
			dto.Name = "Módulo " + dto.ID
		}
		items = append(items, dto)
	}
	return items, total, rows.Err()
}

func (s *Server) portalListPhases(ctx context.Context, moduleID int64, p portalPagination) ([]portalPhaseDTO, int64, error) {
	args := []any{moduleID}
	where := "WHERE module_id = $1"
	if p.Query != "" {
		args = append(args, "%"+p.Query+"%")
		where += fmt.Sprintf(" AND COALESCE(name, '') ILIKE $%d", len(args))
	}
	var total int64
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM phase `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT id::text, module_id::text, COALESCE(name, ''), week_number, index_order, admin_authorize, created_at, updated_at
		FROM phase %s ORDER BY index_order ASC, id ASC LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalPhaseDTO{}
	for rows.Next() {
		var dto portalPhaseDTO
		if err := rows.Scan(&dto.ID, &dto.ModuleID, &dto.Name, &dto.WeekNumber, &dto.IndexOrder, &dto.AdminAuthorize, &dto.CreatedAt, &dto.UpdatedAt); err != nil {
			return nil, 0, err
		}
		if dto.Name == "" {
			dto.Name = "Fase " + dto.ID
		}
		items = append(items, dto)
	}
	return items, total, rows.Err()
}

func portalNowUTC() time.Time { return time.Now().UTC() }

// ── Catálogo: mutações ───────────────────────────────────────────────────────

func (s *Server) portalCreateCourse(ctx context.Context, in portalCourseInput) (*portalCourseDTO, error) {
	isPaid := false
	if in.IsPaid != nil {
		isPaid = *in.IsPaid
	}
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO course (name, description, is_paid, duration_hours, level, focus, price, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,NOW(),NOW()) RETURNING id`,
		in.Name, in.Description, isPaid, in.DurationHours, in.Level, in.Focus, in.Price).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.portalGetCourse(ctx, id)
}

func (s *Server) portalUpdateCourse(ctx context.Context, id int64, in portalCourseInput) (*portalCourseDTO, error) {
	tag, err := s.db.Exec(ctx, `UPDATE course SET
		name = COALESCE(NULLIF($2,''), name),
		description = COALESCE($3, description),
		is_paid = COALESCE($4, is_paid),
		duration_hours = COALESCE($5, duration_hours),
		level = COALESCE($6, level),
		focus = COALESCE($7, focus),
		price = COALESCE($8::numeric, price),
		updated_at = NOW()
		WHERE id=$1`, id, in.Name, in.Description, in.IsPaid, in.DurationHours, in.Level, in.Focus, in.Price)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, notFoundErr("Curso")
	}
	return s.portalGetCourse(ctx, id)
}

// portalDeleteByID apaga uma linha de course/module/phase por id. A allowlist de
// tabelas evita injeção no nome da tabela (interpolado, não parametrizável).
func (s *Server) portalDeleteByID(ctx context.Context, table string, id int64) error {
	if table != "course" && table != "module" && table != "phase" {
		return validationErr("tabela inválida")
	}
	tag, err := s.db.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id=$1`, table), id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr("Registro")
	}
	return nil
}

func (s *Server) portalCreateModule(ctx context.Context, courseID int64, in portalModuleInput) (*portalModuleDTO, error) {
	indexOrder := in.IndexOrder
	if indexOrder == nil {
		var next int
		if err := s.db.QueryRow(ctx, `SELECT COALESCE(MAX(index_order), 0) + 1 FROM module WHERE course_id=$1`, courseID).Scan(&next); err != nil {
			return nil, err
		}
		indexOrder = &next
	}
	var dto portalModuleDTO
	err := s.db.QueryRow(ctx, `INSERT INTO module (course_id, name, description, index_order, created_at, updated_at)
		VALUES ($1,$2,$3,$4,NOW(),NOW())
		RETURNING id::text, course_id::text, COALESCE(name,''), description, index_order`,
		courseID, in.Name, in.Description, *indexOrder).Scan(&dto.ID, &dto.CourseID, &dto.Name, &dto.Description, &dto.IndexOrder)
	if err != nil {
		return nil, err
	}
	if dto.Name == "" {
		dto.Name = "Módulo " + dto.ID
	}
	return &dto, nil
}

func (s *Server) portalUpdateModule(ctx context.Context, moduleID int64, in portalModuleInput) (*portalModuleDTO, error) {
	var dto portalModuleDTO
	err := s.db.QueryRow(ctx, `UPDATE module SET
		name=COALESCE(NULLIF($2,''), name),
		description=COALESCE($3, description),
		index_order=COALESCE($4, index_order),
		updated_at=NOW()
		WHERE id=$1
		RETURNING id::text, course_id::text, COALESCE(name,''), description, index_order`,
		moduleID, in.Name, in.Description, in.IndexOrder).Scan(&dto.ID, &dto.CourseID, &dto.Name, &dto.Description, &dto.IndexOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundErr("Módulo")
	}
	if err != nil {
		return nil, err
	}
	if dto.Name == "" {
		dto.Name = "Módulo " + dto.ID
	}
	return &dto, nil
}

func (s *Server) portalCreatePhase(ctx context.Context, moduleID int64, in portalPhaseInput) (*portalPhaseDTO, error) {
	indexOrder := in.IndexOrder
	if indexOrder == nil {
		var next int
		if err := s.db.QueryRow(ctx, `SELECT COALESCE(MAX(index_order), 0) + 1 FROM phase WHERE module_id=$1`, moduleID).Scan(&next); err != nil {
			return nil, err
		}
		indexOrder = &next
	}
	weekNumber := indexOrder
	if in.WeekNumber != nil {
		weekNumber = in.WeekNumber
	}
	adminAuthorize := true
	if in.AdminAuthorize != nil {
		adminAuthorize = *in.AdminAuthorize
	}
	var dto portalPhaseDTO
	err := s.db.QueryRow(ctx, `INSERT INTO phase (module_id, name, week_number, index_order, admin_authorize, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,NOW(),NOW())
		RETURNING id::text, module_id::text, COALESCE(name,''), week_number, index_order, admin_authorize, created_at, updated_at`,
		moduleID, in.Name, *weekNumber, *indexOrder, adminAuthorize).Scan(&dto.ID, &dto.ModuleID, &dto.Name, &dto.WeekNumber, &dto.IndexOrder, &dto.AdminAuthorize, &dto.CreatedAt, &dto.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if dto.Name == "" {
		dto.Name = "Fase " + dto.ID
	}
	return &dto, nil
}

func (s *Server) portalUpdatePhase(ctx context.Context, phaseID int64, in portalPhaseInput) (*portalPhaseDTO, error) {
	var dto portalPhaseDTO
	err := s.db.QueryRow(ctx, `UPDATE phase SET
		name=COALESCE(NULLIF($2,''), name),
		week_number=COALESCE($3, week_number),
		index_order=COALESCE($4, index_order),
		admin_authorize=COALESCE($5, admin_authorize),
		updated_at=NOW()
		WHERE id=$1
		RETURNING id::text, module_id::text, COALESCE(name,''), week_number, index_order, admin_authorize, created_at, updated_at`,
		phaseID, in.Name, in.WeekNumber, in.IndexOrder, in.AdminAuthorize).Scan(&dto.ID, &dto.ModuleID, &dto.Name, &dto.WeekNumber, &dto.IndexOrder, &dto.AdminAuthorize, &dto.CreatedAt, &dto.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundErr("Fase")
	}
	if err != nil {
		return nil, err
	}
	if dto.Name == "" {
		dto.Name = "Fase " + dto.ID
	}
	return &dto, nil
}
