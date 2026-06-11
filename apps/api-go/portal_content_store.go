package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// portalMergeModuleDesc recompõe a description com o metadado de módulo,
// preservando o módulo/description atuais quando o PATCH não os reenvia. Evita
// que um update parcial (ex.: só a description) apague o vínculo de módulo.
func portalMergeModuleDesc(curDesc string, curModuleID, curModuleName *string, in portalMaterialModule) string {
	desc := curDesc
	if strings.TrimSpace(in.desc) != "" {
		desc = strings.TrimSpace(in.desc)
	}
	moduleID := in.moduleID
	if moduleID == nil && curModuleID != nil {
		if v, err := strconv.ParseInt(*curModuleID, 10, 64); err == nil {
			moduleID = &v
		}
	}
	moduleName := in.moduleName
	if moduleName == nil {
		moduleName = curModuleName
	}
	return composeModuleMeta(desc, moduleID, moduleName)
}

// portalMaterialModule são os campos de módulo/description de entrada (material
// ou vídeo) usados no merge.
type portalMaterialModule struct {
	desc       string
	moduleID   *int64
	moduleName *string
}

// ── Exercícios ───────────────────────────────────────────────────────────────

const portalExerciseCols = `id::text, phase_id::text, COALESCE(title,''), COALESCE(description,''),
	COALESCE(type_exercise,2), COALESCE(difficulty,1), COALESCE(index_order,1),
	COALESCE(is_daily_task,false), COALESCE(is_final_exercise,false), COALESCE(points_redeem,0),
	video_url, term_at, created_at, updated_at`

func scanExercise(row pgx.Row) (*portalExerciseDTO, error) {
	var dto portalExerciseDTO
	if err := row.Scan(&dto.ID, &dto.PhaseID, &dto.Title, &dto.Description, &dto.Type, &dto.Difficulty,
		&dto.IndexOrder, &dto.IsDailyTask, &dto.IsFinalExercise, &dto.PointsRedeem, &dto.VideoURL,
		&dto.TermAt, &dto.CreatedAt, &dto.UpdatedAt); err != nil {
		return nil, err
	}
	if dto.Title == "" {
		dto.Title = "Exercício " + dto.ID
	}
	return &dto, nil
}

func (s *Server) portalListExercises(ctx context.Context, phaseID int64, p portalPagination, dailyTask *bool) ([]portalExerciseDTO, int64, error) {
	args := []any{phaseID}
	where := "WHERE phase_id = $1"
	if dailyTask != nil {
		args = append(args, *dailyTask)
		where += fmt.Sprintf(" AND COALESCE(is_daily_task,false) = $%d", len(args))
	}
	if p.Query != "" {
		args = append(args, "%"+p.Query+"%")
		where += fmt.Sprintf(" AND (COALESCE(title,'') ILIKE $%d OR COALESCE(description,'') ILIKE $%d)", len(args), len(args))
	}
	var total int64
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM exercise `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT %s FROM exercise %s
		ORDER BY index_order ASC, id ASC LIMIT $%d OFFSET $%d`, portalExerciseCols, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalExerciseDTO{}
	for rows.Next() {
		dto, err := scanExercise(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *dto)
	}
	return items, total, rows.Err()
}

func (s *Server) portalExerciseQuestions(ctx context.Context, exerciseID int64) ([]portalQuestionDTO, error) {
	rows, err := s.db.Query(ctx, `SELECT q.id::text, COALESCE(q.statement,''), o.id::text, COALESCE(o.option_text,''), COALESCE(o.is_correct,false)
		FROM question q
		LEFT JOIN question_option o ON o.question_id = q.id
		WHERE q.exercise_id = $1
		ORDER BY q.id ASC, o.id ASC`, exerciseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	questions := []portalQuestionDTO{}
	idx := map[string]int{}
	for rows.Next() {
		var qid, stmt string
		var oid, otext *string
		var correct *bool
		if err := rows.Scan(&qid, &stmt, &oid, &otext, &correct); err != nil {
			return nil, err
		}
		pos, ok := idx[qid]
		if !ok {
			questions = append(questions, portalQuestionDTO{ID: qid, Statement: stmt, Options: []portalOptionDTO{}})
			pos = len(questions) - 1
			idx[qid] = pos
		}
		if oid != nil {
			opt := portalOptionDTO{ID: *oid}
			if otext != nil {
				opt.Text = *otext
			}
			if correct != nil {
				opt.IsCorrect = *correct
			}
			questions[pos].Options = append(questions[pos].Options, opt)
		}
	}
	return questions, rows.Err()
}

func (s *Server) portalGetExercise(ctx context.Context, id int64) (*portalExerciseDTO, error) {
	dto, err := scanExercise(s.db.QueryRow(ctx, `SELECT `+portalExerciseCols+` FROM exercise WHERE id=$1`, id))
	if err != nil {
		return nil, err
	}
	questions, err := s.portalExerciseQuestions(ctx, id)
	if err != nil {
		return nil, err
	}
	dto.Questions = questions
	return dto, nil
}

// syncExerciseQuestions substitui todas as questões/opções do exercício pelas
// fornecidas. A letra (A/B/C) é posicional pela ordem de inserção.
func (s *Server) syncExerciseQuestions(ctx context.Context, tx pgx.Tx, exerciseID int64, questions []portalQuestionInput) error {
	if _, err := tx.Exec(ctx, `DELETE FROM question_option WHERE question_id IN (SELECT id FROM question WHERE exercise_id=$1)`, exerciseID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM question WHERE exercise_id=$1`, exerciseID); err != nil {
		return err
	}
	for _, q := range questions {
		var qid int64
		if err := tx.QueryRow(ctx, `INSERT INTO question (statement, exercise_id) VALUES ($1,$2) RETURNING id`, q.Statement, exerciseID).Scan(&qid); err != nil {
			return err
		}
		for _, o := range q.Options {
			if _, err := tx.Exec(ctx, `INSERT INTO question_option (question_id, option_text, is_correct) VALUES ($1,$2,$3)`, qid, o.Text, o.IsCorrect); err != nil {
				return err
			}
		}
	}
	return nil
}

func portalExerciseDefaults(in portalExerciseInput) (typ, difficulty, points int, isDaily, isFinal bool) {
	typ, difficulty, points = 2, 1, 0
	if in.Type != nil {
		typ = *in.Type
	}
	if in.Difficulty != nil {
		difficulty = *in.Difficulty
	}
	if in.PointsRedeem != nil {
		points = *in.PointsRedeem
	}
	if in.IsDailyTask != nil {
		isDaily = *in.IsDailyTask
	}
	if in.IsFinalExercise != nil {
		isFinal = *in.IsFinalExercise
	}
	return
}

func portalTermAt(raw *string) (*time.Time, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	t, err := portalParseDate(*raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Server) portalCreateExercise(ctx context.Context, phaseID int64, in portalExerciseInput) (*portalExerciseDTO, error) {
	typ, difficulty, points, isDaily, isFinal := portalExerciseDefaults(in)
	termAt, err := portalTermAt(in.TermAt)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	indexOrder := in.IndexOrder
	if indexOrder == nil {
		var next int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(index_order),0)+1 FROM exercise WHERE phase_id=$1`, phaseID).Scan(&next); err != nil {
			return nil, err
		}
		indexOrder = &next
	}

	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO exercise
		(title, description, phase_id, type_exercise, difficulty, index_order, is_daily_task, is_final_exercise, points_redeem, video_url, term_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NOW(),NOW()) RETURNING id`,
		in.Title, in.Description, phaseID, typ, difficulty, *indexOrder, isDaily, isFinal, points, in.VideoURL, termAt).Scan(&id)
	if err != nil {
		return nil, portalDBErr(err)
	}
	if typ == 1 {
		if err := s.syncExerciseQuestions(ctx, tx, id, in.Questions); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.portalGetExercise(ctx, id)
}

func (s *Server) portalUpdateExercise(ctx context.Context, id int64, in portalExerciseInput) (*portalExerciseDTO, error) {
	termAt, err := portalTermAt(in.TermAt)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `UPDATE exercise SET
		title=COALESCE(NULLIF($2,''), title),
		description=COALESCE(NULLIF($3,''), description),
		type_exercise=COALESCE($4, type_exercise),
		difficulty=COALESCE($5, difficulty),
		index_order=COALESCE($6, index_order),
		is_daily_task=COALESCE($7, is_daily_task),
		is_final_exercise=COALESCE($8, is_final_exercise),
		points_redeem=COALESCE($9, points_redeem),
		video_url=COALESCE($10, video_url),
		term_at=COALESCE($11, term_at),
		updated_at=NOW()
		WHERE id=$1`,
		id, in.Title, in.Description, in.Type, in.Difficulty, in.IndexOrder, in.IsDailyTask, in.IsFinalExercise, in.PointsRedeem, in.VideoURL, termAt)
	if err != nil {
		return nil, portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, notFoundErr("Exercício")
	}

	// só mexe nas questões se o corpo enviou questões; bloqueia se já há respostas.
	// (lista vazia = "não mexer", não "apagar tudo")
	if len(in.Questions) > 0 {
		var n int64
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM answer WHERE exercise_id=$1`, id).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, conflictErr("exercício já tem respostas; não é possível alterar as questões")
		}
		if err := s.syncExerciseQuestions(ctx, tx, id, in.Questions); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.portalGetExercise(ctx, id)
}

func (s *Server) portalDeleteExercise(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM exercise WHERE id=$1`, id)
	if err != nil {
		return portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr("Exercício")
	}
	return nil
}

// ── Containers ───────────────────────────────────────────────────────────────

func (s *Server) portalListContainers(ctx context.Context, phaseID int64) ([]portalContainerGroupDTO, error) {
	rows, err := s.db.Query(ctx, `SELECT ct.id::text, ct.exercise_id::text, COALESCE(ct.name,''),
		COALESCE(ct.is_daily_task,false), ct.container_date_target_int,
		COALESCE(e.title,''), COALESCE(e.description,''), COALESCE(e.index_order,1)
		FROM container_tasks ct JOIN exercise e ON e.id = ct.exercise_id
		WHERE ct.phase_id=$1
		ORDER BY ct.container_date_target_int ASC NULLS LAST, ct.name ASC, e.index_order ASC, ct.id ASC`, phaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := []portalContainerGroupDTO{}
	idx := map[string]int{}
	for rows.Next() {
		var ctID, exID, name, title, desc string
		var isDaily bool
		var dateTarget *int
		var indexOrder int
		if err := rows.Scan(&ctID, &exID, &name, &isDaily, &dateTarget, &title, &desc, &indexOrder); err != nil {
			return nil, err
		}
		key := portalContainerKey(name, isDaily, dateTarget)
		pos, ok := idx[key]
		if !ok {
			groups = append(groups, portalContainerGroupDTO{
				Name: name, PhaseID: fmt.Sprint(phaseID), IsDailyTask: isDaily,
				ContainerDateTargetInt: dateTarget, Exercises: []portalContainerExerciseDTO{},
			})
			pos = len(groups) - 1
			idx[key] = pos
		}
		groups[pos].Exercises = append(groups[pos].Exercises, portalContainerExerciseDTO{
			ContainerTaskID: ctID, ExerciseID: exID, Title: title, Description: desc, IndexOrder: indexOrder,
		})
	}
	return groups, rows.Err()
}

func portalContainerKey(name string, isDaily bool, dateTarget *int) string {
	d := "null"
	if dateTarget != nil {
		d = fmt.Sprint(*dateTarget)
	}
	return fmt.Sprintf("%s|%t|%s", name, isDaily, d)
}

func (s *Server) portalExerciseContainer(ctx context.Context, exerciseID int64) (*portalContainerGroupRef, error) {
	var ref portalContainerGroupRef
	var name string
	err := s.db.QueryRow(ctx, `SELECT COALESCE(name,''), phase_id, COALESCE(is_daily_task,false), container_date_target_int
		FROM container_tasks WHERE exercise_id=$1 LIMIT 1`, exerciseID).Scan(&name, &ref.PhaseID, &ref.IsDailyTask, &ref.ContainerDateTargetInt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ref.Name = name
	return &ref, nil
}

func (s *Server) portalCreateContainer(ctx context.Context, in portalContainerInput) (int, error) {
	isDaily := false
	if in.IsDailyTask != nil {
		isDaily = *in.IsDailyTask
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	count := 0
	for _, exID := range in.ExerciseIDs {
		// um exercício só pode estar em um container por fase
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM container_tasks WHERE exercise_id=$1 AND phase_id=$2)`, exID, in.PhaseID).Scan(&exists); err != nil {
			return 0, err
		}
		if exists {
			return 0, conflictErr(fmt.Sprintf("exercício %d já está em um container desta fase", exID))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO container_tasks (name, exercise_id, phase_id, is_daily_task, container_date_target_int, created_at)
			VALUES ($1,$2,$3,$4,$5,NOW())`, in.Name, exID, in.PhaseID, isDaily, in.ContainerDateTargetInt); err != nil {
			return 0, portalDBErr(err)
		}
		count++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Server) portalAddContainerExercises(ctx context.Context, ref portalContainerGroupRef, exerciseIDs []int64) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// grupo precisa existir
	var groupExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM container_tasks
		WHERE name=$1 AND phase_id=$2 AND COALESCE(is_daily_task,false)=$3
		AND (container_date_target_int=$4 OR ($4 IS NULL AND container_date_target_int IS NULL)))`,
		ref.Name, ref.PhaseID, ref.IsDailyTask, ref.ContainerDateTargetInt).Scan(&groupExists); err != nil {
		return 0, err
	}
	if !groupExists {
		return 0, notFoundErr("Container")
	}

	count := 0
	for _, exID := range exerciseIDs {
		var inPhase bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM exercise WHERE id=$1 AND phase_id=$2)`, exID, ref.PhaseID).Scan(&inPhase); err != nil {
			return 0, err
		}
		if !inPhase {
			return 0, validationErr(fmt.Sprintf("exercício %d não pertence à fase", exID))
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM container_tasks WHERE exercise_id=$1 AND phase_id=$2)`, exID, ref.PhaseID).Scan(&exists); err != nil {
			return 0, err
		}
		if exists {
			return 0, conflictErr(fmt.Sprintf("exercício %d já está em um container desta fase", exID))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO container_tasks (name, exercise_id, phase_id, is_daily_task, container_date_target_int, created_at)
			VALUES ($1,$2,$3,$4,$5,NOW())`, ref.Name, exID, ref.PhaseID, ref.IsDailyTask, ref.ContainerDateTargetInt); err != nil {
			return 0, portalDBErr(err)
		}
		count++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Server) portalDeleteContainerGroup(ctx context.Context, ref portalContainerGroupRef) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM container_tasks
		WHERE name=$1 AND phase_id=$2 AND COALESCE(is_daily_task,false)=$3
		AND (container_date_target_int=$4 OR ($4 IS NULL AND container_date_target_int IS NULL))`,
		ref.Name, ref.PhaseID, ref.IsDailyTask, ref.ContainerDateTargetInt)
	if err != nil {
		return 0, portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return 0, notFoundErr("Container")
	}
	return tag.RowsAffected(), nil
}

func (s *Server) portalDeleteContainerTask(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM container_tasks WHERE id=$1`, id)
	if err != nil {
		return portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr("Container")
	}
	return nil
}

// ── Materiais ────────────────────────────────────────────────────────────────

func materialFromRow(id, courseID, title, rawDesc string, fileURL, fileType *string, createdAt time.Time) portalMaterialDTO {
	desc, moduleID, moduleName := parseModuleMeta(rawDesc)
	return portalMaterialDTO{
		ID: id, CourseID: courseID, Title: title, Description: desc,
		ModuleID: moduleID, Module: moduleName, FileURL: fileURL, FileType: fileType, CreatedAt: createdAt,
	}
}

func (s *Server) portalListMaterials(ctx context.Context, courseID int64, p portalPagination) ([]portalMaterialDTO, int64, error) {
	args := []any{courseID}
	where := "WHERE course_id = $1"
	if p.Query != "" {
		args = append(args, "%"+p.Query+"%")
		where += fmt.Sprintf(" AND (COALESCE(title,'') ILIKE $%d OR COALESCE(description,'') ILIKE $%d)", len(args), len(args))
	}
	var total int64
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM material `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT id::text, course_id::text, COALESCE(title,''), COALESCE(description,''), file_url, file_type, uploaded_at
		FROM material %s ORDER BY uploaded_at DESC LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalMaterialDTO{}
	for rows.Next() {
		var id, courseIDStr, title, desc string
		var fileURL, fileType *string
		var createdAt time.Time
		if err := rows.Scan(&id, &courseIDStr, &title, &desc, &fileURL, &fileType, &createdAt); err != nil {
			return nil, 0, err
		}
		items = append(items, materialFromRow(id, courseIDStr, title, desc, fileURL, fileType, createdAt))
	}
	return items, total, rows.Err()
}

func (s *Server) portalGetMaterial(ctx context.Context, id int64) (*portalMaterialDTO, error) {
	var mid, courseIDStr, title, desc string
	var fileURL, fileType *string
	var createdAt time.Time
	err := s.db.QueryRow(ctx, `SELECT id::text, course_id::text, COALESCE(title,''), COALESCE(description,''), file_url, file_type, uploaded_at
		FROM material WHERE id=$1`, id).Scan(&mid, &courseIDStr, &title, &desc, &fileURL, &fileType, &createdAt)
	if err != nil {
		return nil, err
	}
	dto := materialFromRow(mid, courseIDStr, title, desc, fileURL, fileType, createdAt)
	return &dto, nil
}

func (s *Server) portalCreateMaterial(ctx context.Context, courseID int64, in portalMaterialInput) (*portalMaterialDTO, error) {
	desc := composeModuleMeta(strings.TrimSpace(in.Description), in.ModuleID, in.Module)
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO material (course_id, title, description, file_url, file_type, visibility, uploaded_at)
		VALUES ($1,$2,$3,$4,$5,1,NOW()) RETURNING id`,
		courseID, in.Title, desc, in.FileURL, in.FileType).Scan(&id)
	if err != nil {
		return nil, portalDBErr(err)
	}
	return s.portalGetMaterial(ctx, id)
}

func (s *Server) portalUpdateMaterial(ctx context.Context, id int64, in portalMaterialInput) (*portalMaterialDTO, error) {
	existing, err := s.portalGetMaterial(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundErr("Material")
	}
	if err != nil {
		return nil, err
	}
	desc := portalMergeModuleDesc(existing.Description, existing.ModuleID, existing.Module,
		portalMaterialModule{desc: in.Description, moduleID: in.ModuleID, moduleName: in.Module})
	_, err = s.db.Exec(ctx, `UPDATE material SET
		title = COALESCE(NULLIF($2,''), title),
		description = $3,
		file_url = COALESCE($4, file_url),
		file_type = COALESCE($5, file_type)
		WHERE id=$1`, id, in.Title, desc, in.FileURL, in.FileType)
	if err != nil {
		return nil, portalDBErr(err)
	}
	return s.portalGetMaterial(ctx, id)
}

func (s *Server) portalDeleteMaterial(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM material WHERE id=$1`, id)
	if err != nil {
		return portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr("Material")
	}
	return nil
}

// ── Vídeos ───────────────────────────────────────────────────────────────────

func videoFromRow(id, title, rawDesc, url string, thumb *string, duration *int, createdAt time.Time) portalVideoDTO {
	desc, moduleID, moduleName := parseModuleMeta(rawDesc)
	return portalVideoDTO{
		ID: id, Title: title, Description: desc, ModuleID: moduleID, Module: moduleName,
		URL: url, ThumbnailURL: thumb, DurationSeconds: duration, CreatedAt: createdAt,
	}
}

func (s *Server) portalListVideos(ctx context.Context, p portalPagination) ([]portalVideoDTO, int64, error) {
	args := []any{}
	where := ""
	if p.Query != "" {
		args = append(args, "%"+p.Query+"%")
		where = "WHERE COALESCE(title,'') ILIKE $1 OR COALESCE(description,'') ILIKE $1"
	}
	var total int64
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM video `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT id::text, COALESCE(title,''), COALESCE(description,''), COALESCE(url,''), thumbnail_url, duration_seconds, created_at
		FROM video %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalVideoDTO{}
	for rows.Next() {
		var id, title, desc, url string
		var thumb *string
		var duration *int
		var createdAt time.Time
		if err := rows.Scan(&id, &title, &desc, &url, &thumb, &duration, &createdAt); err != nil {
			return nil, 0, err
		}
		items = append(items, videoFromRow(id, title, desc, url, thumb, duration, createdAt))
	}
	return items, total, rows.Err()
}

func (s *Server) portalGetVideo(ctx context.Context, id int64) (*portalVideoDTO, error) {
	var vid, title, desc, url string
	var thumb *string
	var duration *int
	var createdAt time.Time
	err := s.db.QueryRow(ctx, `SELECT id::text, COALESCE(title,''), COALESCE(description,''), COALESCE(url,''), thumbnail_url, duration_seconds, created_at
		FROM video WHERE id=$1`, id).Scan(&vid, &title, &desc, &url, &thumb, &duration, &createdAt)
	if err != nil {
		return nil, err
	}
	dto := videoFromRow(vid, title, desc, url, thumb, duration, createdAt)
	return &dto, nil
}

func (s *Server) portalCreateVideo(ctx context.Context, in portalVideoInput) (*portalVideoDTO, error) {
	desc := composeModuleMeta(strings.TrimSpace(in.Description), in.ModuleID, in.Module)
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO video (title, description, url, thumbnail_url, duration_seconds, visibility, created_at)
		VALUES ($1,$2,$3,$4,$5,1,NOW()) RETURNING id`,
		in.Title, desc, in.URL, in.ThumbnailURL, in.DurationSeconds).Scan(&id)
	if err != nil {
		return nil, portalDBErr(err)
	}
	return s.portalGetVideo(ctx, id)
}

func (s *Server) portalUpdateVideo(ctx context.Context, id int64, in portalVideoInput) (*portalVideoDTO, error) {
	existing, err := s.portalGetVideo(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundErr("Vídeo")
	}
	if err != nil {
		return nil, err
	}
	desc := portalMergeModuleDesc(existing.Description, existing.ModuleID, existing.Module,
		portalMaterialModule{desc: in.Description, moduleID: in.ModuleID, moduleName: in.Module})
	_, err = s.db.Exec(ctx, `UPDATE video SET
		title = COALESCE(NULLIF($2,''), title),
		description = $3,
		url = COALESCE(NULLIF($4,''), url),
		thumbnail_url = COALESCE($5, thumbnail_url),
		duration_seconds = COALESCE($6, duration_seconds)
		WHERE id=$1`, id, in.Title, desc, in.URL, in.ThumbnailURL, in.DurationSeconds)
	if err != nil {
		return nil, portalDBErr(err)
	}
	return s.portalGetVideo(ctx, id)
}

func (s *Server) portalDeleteVideo(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM video WHERE id=$1`, id)
	if err != nil {
		return portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr("Vídeo")
	}
	return nil
}
