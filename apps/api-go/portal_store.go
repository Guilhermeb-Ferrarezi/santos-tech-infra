package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// portalDBErr converte erros de integridade do Postgres num 409/400 legível;
// qualquer outro erro passa intacto (writeErr → 500). Isso evita que IDs de FK
// inexistentes (curso/módulo/turma/aluno) virem um 500 genérico.
func portalDBErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23503": // foreign_key_violation
			return conflictErr("referência inválida (curso, módulo, turma ou aluno inexistente)")
		case "23505": // unique_violation
			return conflictErr("registro duplicado")
		case "23514": // check_violation
			return validationErr("valor viola uma restrição do banco")
		}
	}
	return err
}

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
	err := s.portalDB.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM course),
		(SELECT COUNT(*) FROM module),
		(SELECT COUNT(*) FROM phase),
		(SELECT COUNT(*) FROM class),
		(SELECT COUNT(*) FROM class_rooms)`).Scan(&out.Courses, &out.Modules, &out.Phases, &out.Classes, &out.Rooms)
	return out, err
}

// portalSearchUsers busca usuários do portal (tabela "user") por nome/email,
// para os seletores de aluno (atribuir medalha/meta, matricular em turma).
func (s *Server) portalSearchUsers(ctx context.Context, q string, limit int) ([]portalStudentDTO, error) {
	rows, err := s.portalDB.Query(ctx, `SELECT id::text, COALESCE(email,''), COALESCE(name,''), COALESCE(role,1)
		FROM "user"
		WHERE ($1 = '' OR name ILIKE $2 OR email ILIKE $2)
		ORDER BY name ASC LIMIT $3`, q, "%"+q+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []portalStudentDTO{}
	for rows.Next() {
		var u portalStudentDTO
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role); err != nil {
			return nil, err
		}
		items = append(items, u)
	}
	return items, rows.Err()
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
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM course `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	limitPos := len(args) - 1
	offsetPos := len(args)
	rows, err := s.portalDB.Query(ctx, fmt.Sprintf(`SELECT id, COALESCE(name, ''), description, is_paid, duration_hours, level_difficulty, paid_focus, price::text
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
	err := s.portalDB.QueryRow(ctx, `SELECT id::text, COALESCE(name, ''), description, is_paid, duration_hours, level_difficulty, paid_focus, price::text
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
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM module `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	rows, err := s.portalDB.Query(ctx, fmt.Sprintf(`SELECT id::text, course_id::text, COALESCE(name, ''), description, index_order
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
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM phase `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	rows, err := s.portalDB.Query(ctx, fmt.Sprintf(`SELECT id::text, module_id::text, COALESCE(name, ''), week_number, index_order, admin_authorize, created_at, updated_at
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
	err := s.portalDB.QueryRow(ctx, `INSERT INTO course (name, description, is_paid, duration_hours, level_difficulty, paid_focus, price, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,NOW(),NOW()) RETURNING id`,
		in.Name, in.Description, isPaid, in.DurationHours, in.Level, in.Focus, in.Price).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.portalGetCourse(ctx, id)
}

func (s *Server) portalUpdateCourse(ctx context.Context, id int64, in portalCourseInput) (*portalCourseDTO, error) {
	tag, err := s.portalDB.Exec(ctx, `UPDATE course SET
		name = COALESCE(NULLIF($2,''), name),
		description = COALESCE($3, description),
		is_paid = COALESCE($4, is_paid),
		duration_hours = COALESCE($5, duration_hours),
		level_difficulty = COALESCE($6, level_difficulty),
		paid_focus = COALESCE($7, paid_focus),
		price = COALESCE(NULLIF($8, '')::numeric, price),
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
	tag, err := s.portalDB.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id=$1`, table), id)
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
		if err := s.portalDB.QueryRow(ctx, `SELECT COALESCE(MAX(index_order), 0) + 1 FROM module WHERE course_id=$1`, courseID).Scan(&next); err != nil {
			return nil, err
		}
		indexOrder = &next
	}
	var dto portalModuleDTO
	err := s.portalDB.QueryRow(ctx, `INSERT INTO module (course_id, name, description, index_order, created_at, updated_at)
		VALUES ($1,$2,$3,$4,NOW(),NOW())
		RETURNING id::text, course_id::text, COALESCE(name,''), description, index_order`,
		courseID, in.Name, in.Description, *indexOrder).Scan(&dto.ID, &dto.CourseID, &dto.Name, &dto.Description, &dto.IndexOrder)
	if err != nil {
		return nil, portalDBErr(err)
	}
	if dto.Name == "" {
		dto.Name = "Módulo " + dto.ID
	}
	return &dto, nil
}

func (s *Server) portalUpdateModule(ctx context.Context, moduleID int64, in portalModuleInput) (*portalModuleDTO, error) {
	var dto portalModuleDTO
	err := s.portalDB.QueryRow(ctx, `UPDATE module SET
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
		if err := s.portalDB.QueryRow(ctx, `SELECT COALESCE(MAX(index_order), 0) + 1 FROM phase WHERE module_id=$1`, moduleID).Scan(&next); err != nil {
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
	err := s.portalDB.QueryRow(ctx, `INSERT INTO phase (module_id, name, week_number, index_order, admin_authorize, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,NOW(),NOW())
		RETURNING id::text, module_id::text, COALESCE(name,''), week_number, index_order, admin_authorize, created_at, updated_at`,
		moduleID, in.Name, *weekNumber, *indexOrder, adminAuthorize).Scan(&dto.ID, &dto.ModuleID, &dto.Name, &dto.WeekNumber, &dto.IndexOrder, &dto.AdminAuthorize, &dto.CreatedAt, &dto.UpdatedAt)
	if err != nil {
		return nil, portalDBErr(err)
	}
	if dto.Name == "" {
		dto.Name = "Fase " + dto.ID
	}
	return &dto, nil
}

func (s *Server) portalUpdatePhase(ctx context.Context, phaseID int64, in portalPhaseInput) (*portalPhaseDTO, error) {
	var dto portalPhaseDTO
	err := s.portalDB.QueryRow(ctx, `UPDATE phase SET
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

// ── Turmas e matrículas ──────────────────────────────────────────────────────

func (s *Server) portalListClasses(ctx context.Context, p portalPagination) ([]portalClassDTO, int64, error) {
	args := []any{}
	where := ""
	if p.Query != "" {
		args = append(args, "%"+p.Query+"%")
		where = "WHERE COALESCE(name, '') ILIKE $1"
	}
	var total int64
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM class `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	rows, err := s.portalDB.Query(ctx, fmt.Sprintf(`SELECT id::text, COALESCE(name,''), course_id::text, current_module_id::text, start_date, end_date, created_at, updated_at
		FROM class %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalClassDTO{}
	for rows.Next() {
		var dto portalClassDTO
		if err := rows.Scan(&dto.ID, &dto.Name, &dto.CourseID, &dto.CurrentModuleID, &dto.StartDate, &dto.EndDate, &dto.CreatedAt, &dto.UpdatedAt); err != nil {
			return nil, 0, err
		}
		if dto.Name == "" {
			dto.Name = "Turma " + dto.ID
		}
		items = append(items, dto)
	}
	return items, total, rows.Err()
}

func portalClassEndDate(start time.Time, weeks int) time.Time {
	return start.AddDate(0, 0, weeks*7)
}

func (s *Server) portalGetClass(ctx context.Context, id int64) (*portalClassDTO, error) {
	var dto portalClassDTO
	err := s.portalDB.QueryRow(ctx, `SELECT id::text, COALESCE(name,''), course_id::text, current_module_id::text, start_date, end_date, created_at, updated_at
		FROM class WHERE id=$1`, id).Scan(&dto.ID, &dto.Name, &dto.CourseID, &dto.CurrentModuleID, &dto.StartDate, &dto.EndDate, &dto.CreatedAt, &dto.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if dto.Name == "" {
		dto.Name = "Turma " + dto.ID
	}
	return &dto, nil
}

func (s *Server) portalCreateClass(ctx context.Context, in portalClassInput) (*portalClassDTO, error) {
	start, err := portalParseDate(in.StartDate)
	if err != nil {
		return nil, err
	}
	end := portalClassEndDate(start, in.DurationWeeks)
	var dto portalClassDTO
	err = s.portalDB.QueryRow(ctx, `INSERT INTO class (name, course_id, current_module_id, start_date, end_date, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,NOW(),NOW())
		RETURNING id::text, COALESCE(name,''), course_id::text, current_module_id::text, start_date, end_date, created_at, updated_at`,
		in.Name, in.CourseID, in.CurrentModuleID, start, end).Scan(&dto.ID, &dto.Name, &dto.CourseID, &dto.CurrentModuleID, &dto.StartDate, &dto.EndDate, &dto.CreatedAt, &dto.UpdatedAt)
	if err != nil {
		return nil, portalDBErr(err)
	}
	return &dto, nil
}

func (s *Server) portalUpdateClass(ctx context.Context, id int64, in portalClassInput) (*portalClassDTO, error) {
	if in.DurationWeeks != 0 && (in.DurationWeeks < 1 || in.DurationWeeks > 104) {
		return nil, validationErr("durationWeeks deve ficar entre 1 e 104")
	}
	var startPtr *time.Time
	if strings.TrimSpace(in.StartDate) != "" {
		start, err := portalParseDate(in.StartDate)
		if err != nil {
			return nil, err
		}
		startPtr = &start
	}
	// end_date recalculado só a partir do que veio: durationWeeks define a duração
	// (a partir do start novo ou atual); startDate sozinho preserva a duração atual
	// deslocando o fim; nenhum dos dois → end_date intacto.
	var dto portalClassDTO
	err := s.portalDB.QueryRow(ctx, `UPDATE class SET
		name=COALESCE(NULLIF($2,''), name),
		course_id=COALESCE(NULLIF($3,0), course_id),
		current_module_id=COALESCE(NULLIF($4,0), current_module_id),
		start_date=COALESCE($5, start_date),
		end_date=CASE
			WHEN $6 > 0 THEN COALESCE($5, start_date) + make_interval(days => $6 * 7)
			WHEN $5 IS NOT NULL THEN end_date + ($5 - start_date)
			ELSE end_date
		END,
		updated_at=NOW()
		WHERE id=$1
		RETURNING id::text, COALESCE(name,''), course_id::text, current_module_id::text, start_date, end_date, created_at, updated_at`,
		id, in.Name, in.CourseID, in.CurrentModuleID, startPtr, in.DurationWeeks).Scan(&dto.ID, &dto.Name, &dto.CourseID, &dto.CurrentModuleID, &dto.StartDate, &dto.EndDate, &dto.CreatedAt, &dto.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundErr("Turma")
	}
	if err != nil {
		return nil, portalDBErr(err)
	}
	if dto.Name == "" {
		dto.Name = "Turma " + dto.ID
	}
	return &dto, nil
}

// portalDeleteClass remove a turma e suas matrículas numa transação (a tabela
// enrollment referencia class).
func (s *Server) portalDeleteClass(ctx context.Context, id int64) error {
	tx, err := s.portalDB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM enrollment WHERE class_id=$1`, id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM class WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr("Turma")
	}
	return tx.Commit(ctx)
}

func (s *Server) portalListClassStudents(ctx context.Context, classID int64) ([]portalStudentDTO, error) {
	rows, err := s.portalDB.Query(ctx, `SELECT u.id::text, COALESCE(u.email,''), COALESCE(u.name,''), u.role
		FROM enrollment e JOIN "user" u ON u.id = e.user_id
		WHERE e.class_id=$1 ORDER BY COALESCE(u.name,'') ASC, u.id ASC`, classID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []portalStudentDTO{}
	for rows.Next() {
		var dto portalStudentDTO
		if err := rows.Scan(&dto.ID, &dto.Email, &dto.Name, &dto.Role); err != nil {
			return nil, err
		}
		items = append(items, dto)
	}
	return items, rows.Err()
}

// portalAddClassStudents matricula os usuários na turma, ignorando duplicatas.
// Devolve quantos foram efetivamente inseridos.
func (s *Server) portalAddClassStudents(ctx context.Context, classID int64, ids []int64) (int, error) {
	tx, err := s.portalDB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	added := 0
	for _, uid := range ids {
		tag, err := tx.Exec(ctx, `INSERT INTO enrollment (user_id, class_id, created_at)
			SELECT $1, $2, NOW()
			WHERE NOT EXISTS (
			  SELECT 1 FROM enrollment WHERE user_id=$1 AND class_id=$2
			)`, uid, classID)
		if err != nil {
			return 0, portalDBErr(err)
		}
		added += int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return added, nil
}

func (s *Server) portalRemoveClassStudent(ctx context.Context, classID, studentID int64) error {
	tag, err := s.portalDB.Exec(ctx, `DELETE FROM enrollment WHERE class_id=$1 AND user_id=$2`, classID, studentID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr("Matrícula")
	}
	return nil
}

// ── Salas (class_rooms) ──────────────────────────────────────────────────────

const portalRoomCols = `id::text, class_id::text, COALESCE(name,''), created_at, is_authorized, target_limited`

func (s *Server) portalListClassRooms(ctx context.Context, classID int64) ([]portalRoomDTO, error) {
	rows, err := s.portalDB.Query(ctx, `SELECT `+portalRoomCols+`
		FROM class_rooms WHERE class_id=$1 ORDER BY created_at DESC`, classID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []portalRoomDTO{}
	for rows.Next() {
		var dto portalRoomDTO
		if err := rows.Scan(&dto.ID, &dto.ClassID, &dto.Name, &dto.CreatedAt, &dto.IsAuthorized, &dto.TargetLimited); err != nil {
			return nil, err
		}
		dto.Status = portalRoomStatus(dto.IsAuthorized, dto.TargetLimited)
		items = append(items, dto)
	}
	return items, rows.Err()
}

// portalRoomStatus deriva o estado da sala: fechada se a data-limite passou,
// aberta se autorizada, senão rascunho.
func portalRoomStatus(open bool, target *time.Time) string {
	if target != nil && target.Before(portalNowUTC()) {
		return "closed"
	}
	if open {
		return "open"
	}
	return "draft"
}

func portalRoomTarget(raw *string) (*time.Time, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	t, err := portalParseDate(*raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Server) portalCreateClassRoom(ctx context.Context, classID int64, in portalRoomInput) (*portalRoomDTO, error) {
	isAuth := false
	if in.IsAuthorized != nil {
		isAuth = *in.IsAuthorized
	}
	target, err := portalRoomTarget(in.TargetLimited)
	if err != nil {
		return nil, err
	}
	var dto portalRoomDTO
	err = s.portalDB.QueryRow(ctx, `INSERT INTO class_rooms (class_id, name, is_authorized, target_limited, created_at)
		VALUES ($1,$2,$3,$4,NOW())
		RETURNING `+portalRoomCols,
		classID, in.Name, isAuth, target).Scan(&dto.ID, &dto.ClassID, &dto.Name, &dto.CreatedAt, &dto.IsAuthorized, &dto.TargetLimited)
	if err != nil {
		return nil, portalDBErr(err)
	}
	dto.Status = portalRoomStatus(dto.IsAuthorized, dto.TargetLimited)
	return &dto, nil
}

func (s *Server) portalUpdateClassRoom(ctx context.Context, roomID int64, in portalRoomInput) (*portalRoomDTO, error) {
	// target_limited só é tocado quanto o campo veio no corpo (setTarget); string
	// vazia limpa (NULL), data válida define.
	setTarget := in.TargetLimited != nil
	target, err := portalRoomTarget(in.TargetLimited)
	if err != nil {
		return nil, err
	}
	var dto portalRoomDTO
	err = s.portalDB.QueryRow(ctx, `UPDATE class_rooms SET
		name = COALESCE(NULLIF($2,''), name),
		is_authorized = COALESCE($3, is_authorized),
		target_limited = CASE WHEN $4 THEN $5::timestamptz ELSE target_limited END
		WHERE id=$1
		RETURNING `+portalRoomCols,
		roomID, in.Name, in.IsAuthorized, setTarget, target).Scan(&dto.ID, &dto.ClassID, &dto.Name, &dto.CreatedAt, &dto.IsAuthorized, &dto.TargetLimited)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundErr("Sala")
	}
	if err != nil {
		return nil, err
	}
	dto.Status = portalRoomStatus(dto.IsAuthorized, dto.TargetLimited)
	return &dto, nil
}

func (s *Server) portalUpdateClassRoomStatus(ctx context.Context, roomID int64, open bool) (*portalRoomDTO, error) {
	var dto portalRoomDTO
	err := s.portalDB.QueryRow(ctx, `UPDATE class_rooms SET is_authorized=$2 WHERE id=$1
		RETURNING `+portalRoomCols, roomID, open).Scan(&dto.ID, &dto.ClassID, &dto.Name, &dto.CreatedAt, &dto.IsAuthorized, &dto.TargetLimited)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundErr("Sala")
	}
	if err != nil {
		return nil, err
	}
	dto.Status = portalRoomStatus(dto.IsAuthorized, dto.TargetLimited)
	return &dto, nil
}

func (s *Server) portalDeleteClassRoom(ctx context.Context, roomID int64) error {
	tag, err := s.portalDB.Exec(ctx, `DELETE FROM class_rooms WHERE id=$1`, roomID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr("Sala")
	}
	return nil
}

// ── Reorder, cronograma e iniciar-fases ──────────────────────────────────────

// portalReorder troca o index_order de uma linha com seu vizinho imediato dentro
// do mesmo escopo (module→course_id, phase→module_id). table/scopeCol vêm de uma
// allowlist (não de input), então a interpolação é segura.
func (s *Server) portalReorder(ctx context.Context, table, scopeCol, entity string, id int64, direction string) error {
	if table != "module" && table != "phase" && table != "exercise" {
		return validationErr("tabela inválida")
	}
	tx, err := s.portalDB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var scope int64
	var order int
	err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s, index_order FROM %s WHERE id=$1`, scopeCol, table), id).Scan(&scope, &order)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFoundErr(entity)
	}
	if err != nil {
		return err
	}

	cmp, ord := "<", "DESC"
	if direction == "down" {
		cmp, ord = ">", "ASC"
	}
	var nid int64
	var norder int
	err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT id, index_order FROM %s WHERE %s=$1 AND index_order %s $2 ORDER BY index_order %s LIMIT 1`, table, scopeCol, cmp, ord), scope, order).Scan(&nid, &norder)
	if errors.Is(err, pgx.ErrNoRows) {
		return validationErr("já está no limite da ordem")
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET index_order=$1, updated_at=NOW() WHERE id=$2`, table), norder, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET index_order=$1, updated_at=NOW() WHERE id=$2`, table), order, nid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// portalClassCronograma deriva o cronograma da turma a partir das fases do módulo
// atual, agrupadas por week_number. Não existe tabela de cronograma no schema.
func (s *Server) portalClassCronograma(ctx context.Context, classID int64) (*portalClassDTO, map[string][]portalCronogramaPhase, error) {
	class, err := s.portalGetClass(ctx, classID)
	if err != nil {
		return nil, nil, err
	}
	moduleID, _ := strconv.ParseInt(class.CurrentModuleID, 10, 64)
	rows, err := s.portalDB.Query(ctx, `SELECT p.id::text, COALESCE(p.name,''), COALESCE(m.name,''), p.week_number
		FROM phase p JOIN module m ON m.id = p.module_id
		WHERE p.module_id=$1
		ORDER BY p.week_number ASC, p.index_order ASC, p.id ASC`, moduleID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	grouped := map[string][]portalCronogramaPhase{}
	for rows.Next() {
		var ph portalCronogramaPhase
		var week int
		if err := rows.Scan(&ph.ID, &ph.Name, &ph.Module, &week); err != nil {
			return nil, nil, err
		}
		if ph.Name == "" {
			ph.Name = "Fase " + ph.ID
		}
		key := strconv.Itoa(week)
		grouped[key] = append(grouped[key], ph)
	}
	return class, grouped, rows.Err()
}

// portalIniciarFases inicializa o progresso dos alunos nas fases do módulo atual
// da turma e desbloqueia a primeira fase. Assume a tabela progress_student_phase
// com colunas (user_id, phase_id, progress, status, unlocked_at, created_at) —
// a API antiga introspeccionava esse schema; aqui assumimos as colunas padrão.
func (s *Server) portalIniciarFases(ctx context.Context, classID int64, studentIDs []int64) (*portalPhaseDTO, int, error) {
	var moduleID int64
	err := s.portalDB.QueryRow(ctx, `SELECT COALESCE(current_module_id, 0) FROM class WHERE id=$1`, classID).Scan(&moduleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, notFoundErr("Turma")
	}
	if err != nil {
		return nil, 0, err
	}
	if moduleID == 0 {
		return nil, 0, validationErr("a turma não tem módulo atual definido")
	}

	tx, err := s.portalDB.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)

	var phase portalPhaseDTO
	err = tx.QueryRow(ctx, `SELECT id::text, module_id::text, COALESCE(name,''), week_number, index_order, admin_authorize, created_at, updated_at
		FROM phase WHERE module_id=$1 ORDER BY index_order ASC, id ASC LIMIT 1`, moduleID).Scan(
		&phase.ID, &phase.ModuleID, &phase.Name, &phase.WeekNumber, &phase.IndexOrder, &phase.AdminAuthorize, &phase.CreatedAt, &phase.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, validationErr("o módulo atual da turma não possui fases")
	}
	if err != nil {
		return nil, 0, err
	}
	if phase.Name == "" {
		phase.Name = "Fase " + phase.ID
	}

	// só alunos efetivamente matriculados na turma
	erows, err := tx.Query(ctx, `SELECT DISTINCT user_id FROM enrollment WHERE class_id=$1 AND user_id = ANY($2)`, classID, studentIDs)
	if err != nil {
		return nil, 0, err
	}
	valid := []int64{}
	for erows.Next() {
		var uid int64
		if err := erows.Scan(&uid); err != nil {
			erows.Close()
			return nil, 0, err
		}
		valid = append(valid, uid)
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return nil, 0, err
	}
	if len(valid) == 0 {
		return nil, 0, validationErr("nenhum aluno inscrito na turma")
	}

	// cria progresso (0/0) para todas as fases do módulo, sem duplicar
	for _, uid := range valid {
		if _, err := tx.Exec(ctx, `INSERT INTO progress_student_phase (user_id, phase_id, progress, status, created_at)
			SELECT $1, p.id, 0, 0, NOW() FROM phase p
			WHERE p.module_id=$2
			AND NOT EXISTS (SELECT 1 FROM progress_student_phase psp WHERE psp.user_id=$1 AND psp.phase_id=p.id)`, uid, moduleID); err != nil {
			return nil, 0, err
		}
	}

	// desbloqueia a primeira fase (status 1 = em progresso; mantém 2 = concluído)
	firstPhaseID, _ := strconv.ParseInt(phase.ID, 10, 64)
	if _, err := tx.Exec(ctx, `UPDATE progress_student_phase
		SET status = CASE WHEN COALESCE(status,0) = 2 THEN 2 ELSE 1 END,
		    unlocked_at = COALESCE(unlocked_at, NOW())
		WHERE user_id = ANY($1) AND phase_id=$2`, valid, firstPhaseID); err != nil {
		return nil, 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return &phase, len(valid), nil
}
