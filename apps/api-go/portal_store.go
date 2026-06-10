package main

import (
	"context"
	"fmt"
	"strings"
	"time"
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
