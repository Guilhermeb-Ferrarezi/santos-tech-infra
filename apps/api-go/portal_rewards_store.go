package main

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// parseNumericText converte um numeric/int do Postgres lido como texto (via
// ::text na query) num *float64, tolerando NULL e colunas int ou numeric sem
// risco de erro de scan. "" → nil.
func parseNumericText(s *string) *float64 {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return nil
	}
	return &v
}

// ── Badges ───────────────────────────────────────────────────────────────────

func (s *Server) portalListBadges(ctx context.Context, p portalPagination) ([]portalBadgeDTO, int64, error) {
	args := []any{}
	where := ""
	if p.Query != "" {
		args = append(args, "%"+p.Query+"%")
		where = "WHERE (name ILIKE $1 OR description ILIKE $1)"
	}
	var total int64
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM badge `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	rows, err := s.portalDB.Query(ctx, `SELECT b.id::text, COALESCE(b.name,''), COALESCE(b.description,''), COALESCE(b.icon_url,''), b.created_at,
		(SELECT COUNT(*) FROM badge_student bs WHERE bs.badge_id = b.id)
		FROM badge b `+where+` ORDER BY b.created_at DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalBadgeDTO{}
	for rows.Next() {
		var b portalBadgeDTO
		if err := rows.Scan(&b.ID, &b.Name, &b.Description, &b.IconURL, &b.CreatedAt, &b.HoldersCount); err != nil {
			return nil, 0, err
		}
		items = append(items, b)
	}
	return items, total, rows.Err()
}

func (s *Server) portalGetBadge(ctx context.Context, id int64) (*portalBadgeDTO, error) {
	var b portalBadgeDTO
	err := s.portalDB.QueryRow(ctx, `SELECT b.id::text, COALESCE(b.name,''), COALESCE(b.description,''), COALESCE(b.icon_url,''), b.created_at,
		(SELECT COUNT(*) FROM badge_student bs WHERE bs.badge_id = b.id)
		FROM badge b WHERE b.id=$1`, id).Scan(&b.ID, &b.Name, &b.Description, &b.IconURL, &b.CreatedAt, &b.HoldersCount)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Server) portalCreateBadge(ctx context.Context, in portalBadgeInput) (*portalBadgeDTO, error) {
	var id int64
	err := s.portalDB.QueryRow(ctx, `INSERT INTO badge (name, description, icon_url, created_at)
		VALUES ($1,$2,$3,NOW()) RETURNING id`,
		strings.TrimSpace(*in.Name), strings.TrimSpace(*in.Description), strings.TrimSpace(*in.IconURL)).Scan(&id)
	if err != nil {
		return nil, portalDBErr(err)
	}
	return s.portalGetBadge(ctx, id)
}

func (s *Server) portalUpdateBadge(ctx context.Context, id int64, in portalBadgeInput) (*portalBadgeDTO, error) {
	var name, desc, icon *string
	if in.Name != nil {
		v := strings.TrimSpace(*in.Name)
		name = &v
	}
	if in.Description != nil {
		v := strings.TrimSpace(*in.Description)
		desc = &v
	}
	if in.IconURL != nil {
		v := strings.TrimSpace(*in.IconURL)
		icon = &v
	}
	tag, err := s.portalDB.Exec(ctx, `UPDATE badge SET
		name = COALESCE($2, name),
		description = COALESCE($3, description),
		icon_url = COALESCE($4, icon_url)
		WHERE id=$1`, id, name, desc, icon)
	if err != nil {
		return nil, portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, notFoundErr("Medalha")
	}
	return s.portalGetBadge(ctx, id)
}

// portalDeleteBadge remove a medalha e seus vínculos (badge_student) em transação.
func (s *Server) portalDeleteBadge(ctx context.Context, id int64) error {
	tx, err := s.portalDB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM badge WHERE id=$1 FOR UPDATE)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return notFoundErr("Medalha")
	}
	// recompensas de meta referenciam badge_id (FK). Limpa-as (e suas
	// atribuições a alunos) antes de remover a medalha, senão o DELETE viola a FK.
	if _, err := tx.Exec(ctx, `DELETE FROM goals_students WHERE goal_reward_id IN (SELECT id FROM goals_rewards WHERE badge_id=$1)`, id); err != nil {
		return portalDBErr(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM goals_rewards WHERE badge_id=$1`, id); err != nil {
		return portalDBErr(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM badge_student WHERE badge_id=$1`, id); err != nil {
		return portalDBErr(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM badge WHERE id=$1`, id); err != nil {
		return portalDBErr(err)
	}
	return tx.Commit(ctx)
}

const portalHolderCols = `bs.id::text, bs.badge_id::text, COALESCE(b.name,''), u.id::text, COALESCE(u.name,''), COALESCE(u.email,''), bs.awarded_at`

func scanHolder(row pgx.Row) (*portalBadgeHolderDTO, error) {
	var h portalBadgeHolderDTO
	if err := row.Scan(&h.HolderID, &h.BadgeID, &h.BadgeName, &h.User.ID, &h.User.Nome, &h.User.Email, &h.AwardedAt); err != nil {
		return nil, err
	}
	return &h, nil
}

func (s *Server) portalListHolders(ctx context.Context, badgeID *int64, p portalPagination) ([]portalBadgeHolderDTO, int64, error) {
	args := []any{badgeID}
	where := "WHERE ($1::bigint IS NULL OR bs.badge_id = $1)"
	var total int64
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM badge_student bs `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	rows, err := s.portalDB.Query(ctx, `SELECT `+portalHolderCols+`
		FROM badge_student bs JOIN badge b ON b.id = bs.badge_id JOIN "user" u ON u.id = bs.user_id
		`+where+` ORDER BY bs.awarded_at DESC LIMIT $2 OFFSET $3`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalBadgeHolderDTO{}
	for rows.Next() {
		h, err := scanHolder(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *h)
	}
	return items, total, rows.Err()
}

func (s *Server) portalAssignBadge(ctx context.Context, userID, badgeID int64) (*portalBadgeHolderDTO, error) {
	var dup bool
	if err := s.portalDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM badge_student WHERE user_id=$1 AND badge_id=$2)`, userID, badgeID).Scan(&dup); err != nil {
		return nil, err
	}
	if dup {
		return nil, conflictErr("usuário já possui essa medalha")
	}
	h, err := scanHolder(s.portalDB.QueryRow(ctx, `WITH inserted AS (
			INSERT INTO badge_student (user_id, badge_id, awarded_at)
			SELECT $1, $2, NOW() FROM "user" u, badge b WHERE u.id=$1 AND b.id=$2
			RETURNING id, user_id, badge_id, awarded_at
		)
		SELECT i.id::text, i.badge_id::text, COALESCE(b.name,''), u.id::text, COALESCE(u.name,''), COALESCE(u.email,''), i.awarded_at
		FROM inserted i JOIN badge b ON b.id = i.badge_id JOIN "user" u ON u.id = i.user_id`, userID, badgeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundErr("Usuário ou medalha")
	}
	if err != nil {
		return nil, portalDBErr(err)
	}
	return h, nil
}

func (s *Server) portalUpdateHolderBadge(ctx context.Context, holderID, badgeID int64) (*portalBadgeHolderDTO, error) {
	h, err := scanHolder(s.portalDB.QueryRow(ctx, `UPDATE badge_student bs
		SET badge_id = $1, awarded_at = NOW()
		FROM badge b, "user" u
		WHERE bs.id = $2 AND b.id = $1 AND u.id = bs.user_id
		RETURNING bs.id::text, bs.badge_id::text, COALESCE(b.name,''), u.id::text, COALESCE(u.name,''), COALESCE(u.email,''), bs.awarded_at`, badgeID, holderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundErr("Vínculo de medalha")
	}
	if err != nil {
		return nil, portalDBErr(err)
	}
	return h, nil
}

func (s *Server) portalDeleteHolder(ctx context.Context, holderID int64) error {
	tag, err := s.portalDB.Exec(ctx, `DELETE FROM badge_student WHERE id=$1`, holderID)
	if err != nil {
		return portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr("Vínculo de medalha")
	}
	return nil
}

// ── Goals ────────────────────────────────────────────────────────────────────

func scanGoal(row pgx.Row) (*portalGoalDTO, error) {
	var g portalGoalDTO
	if err := row.Scan(&g.ID, &g.Name, &g.Description, &g.Type, &g.ImageURL, &g.RewardsCount); err != nil {
		return nil, err
	}
	return &g, nil
}

const portalGoalCols = `g.id::text, COALESCE(g.name,''), g.description, g.type, g.image_url,
	(SELECT COUNT(*) FROM goals_rewards gr WHERE gr.goal_id = g.id)`

func (s *Server) portalListGoals(ctx context.Context, p portalPagination) ([]portalGoalDTO, int64, error) {
	args := []any{}
	where := ""
	if p.Query != "" {
		args = append(args, "%"+p.Query+"%")
		where = "WHERE (COALESCE(g.name,'') ILIKE $1 OR COALESCE(g.description,'') ILIKE $1)"
	}
	var total int64
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM goals g `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset)
	rows, err := s.portalDB.Query(ctx, `SELECT `+portalGoalCols+` FROM goals g `+where+
		` ORDER BY g.id DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalGoalDTO{}
	for rows.Next() {
		g, err := scanGoal(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *g)
	}
	return items, total, rows.Err()
}

func (s *Server) portalGetGoal(ctx context.Context, id int64) (*portalGoalDTO, error) {
	return scanGoal(s.portalDB.QueryRow(ctx, `SELECT `+portalGoalCols+` FROM goals g WHERE g.id=$1`, id))
}

// portalGetGoalWithRewards devolve a meta + suas recompensas (para GET /goals/{id}).
func (s *Server) portalGetGoalWithRewards(ctx context.Context, id int64) (*portalGoalDTO, []portalGoalRewardDTO, error) {
	g, err := s.portalGetGoal(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	rewards, _, err := s.portalListGoalRewards(ctx, &id, nil, portalPagination{Limit: 1000, Offset: 0})
	if err != nil {
		return nil, nil, err
	}
	return g, rewards, nil
}

func (s *Server) portalCreateGoal(ctx context.Context, in portalGoalInput) (*portalGoalDTO, error) {
	var desc, image *string
	if in.Description != nil {
		v := strings.TrimSpace(*in.Description)
		desc = &v
	}
	if in.ImageURL != nil {
		v := strings.TrimSpace(*in.ImageURL)
		image = &v
	}
	var id int64
	err := s.portalDB.QueryRow(ctx, `INSERT INTO goals (name, description, type, image_url)
		VALUES ($1,$2,$3,$4) RETURNING id`,
		strings.TrimSpace(*in.Name), desc, in.Type, image).Scan(&id)
	if err != nil {
		return nil, portalDBErr(err)
	}
	return s.portalGetGoal(ctx, id)
}

func (s *Server) portalUpdateGoal(ctx context.Context, id int64, in portalGoalInput) (*portalGoalDTO, error) {
	var name, desc, image *string
	if in.Name != nil {
		v := strings.TrimSpace(*in.Name)
		name = &v
	}
	if in.Description != nil {
		v := strings.TrimSpace(*in.Description)
		desc = &v
	}
	if in.ImageURL != nil {
		v := strings.TrimSpace(*in.ImageURL)
		image = &v
	}
	tag, err := s.portalDB.Exec(ctx, `UPDATE goals SET
		name = COALESCE($2, name),
		description = COALESCE($3, description),
		type = COALESCE($4, type),
		image_url = COALESCE($5, image_url)
		WHERE id=$1`, id, name, desc, in.Type, image)
	if err != nil {
		return nil, portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, notFoundErr("Meta")
	}
	return s.portalGetGoal(ctx, id)
}

// portalDeleteGoal remove a meta e cascateia goals_students → goals_rewards → goals.
func (s *Server) portalDeleteGoal(ctx context.Context, id int64) error {
	tx, err := s.portalDB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM goals WHERE id=$1 FOR UPDATE)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return notFoundErr("Meta")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM goals_students WHERE goal_reward_id IN (SELECT id FROM goals_rewards WHERE goal_id=$1)`, id); err != nil {
		return portalDBErr(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM goals_rewards WHERE goal_id=$1`, id); err != nil {
		return portalDBErr(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM goals WHERE id=$1`, id); err != nil {
		return portalDBErr(err)
	}
	return tx.Commit(ctx)
}

// ── Goal rewards ─────────────────────────────────────────────────────────────

const portalRewardCols = `gr.id::text, gr.goal_id::text, gr.badge_id::text, gr.course_id::text,
	gr.points::text, gr.created_at, gr.end_date_target, COALESCE(gr.reward_type,0),
	gr.start_date_target, gr.points_target::text,
	COALESCE(g.name,''), COALESCE(b.name,''), b.icon_url, COALESCE(c.name,'')`

func scanReward(row pgx.Row) (*portalGoalRewardDTO, error) {
	var r portalGoalRewardDTO
	var points, pointsTarget *string
	if err := row.Scan(&r.ID, &r.GoalID, &r.BadgeID, &r.CourseID, &points, &r.CreatedAt, &r.EndDateTarget,
		&r.RewardType, &r.StartDateTarget, &pointsTarget, &r.GoalName, &r.BadgeName, &r.BadgeIconURL, &r.CourseName); err != nil {
		return nil, err
	}
	r.Points = parseNumericText(points)
	r.PointsTarget = parseNumericText(pointsTarget)
	return &r, nil
}

const portalRewardJoins = `FROM goals_rewards gr
	JOIN goals g ON g.id = gr.goal_id
	JOIN badge b ON b.id = gr.badge_id
	JOIN course c ON c.id = gr.course_id`

func (s *Server) portalListGoalRewards(ctx context.Context, goalID, courseID *int64, p portalPagination) ([]portalGoalRewardDTO, int64, error) {
	const where = `WHERE ($1::bigint IS NULL OR gr.goal_id = $1) AND ($2::bigint IS NULL OR gr.course_id = $2)`
	var total int64
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM goals_rewards gr `+where, goalID, courseID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.portalDB.Query(ctx, `SELECT `+portalRewardCols+` `+portalRewardJoins+` `+where+`
		ORDER BY gr.created_at DESC, gr.id DESC LIMIT $3 OFFSET $4`, goalID, courseID, p.Limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalGoalRewardDTO{}
	for rows.Next() {
		r, err := scanReward(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *r)
	}
	return items, total, rows.Err()
}

func (s *Server) portalGetReward(ctx context.Context, id int64) (*portalGoalRewardDTO, error) {
	return scanReward(s.portalDB.QueryRow(ctx, `SELECT `+portalRewardCols+` `+portalRewardJoins+` WHERE gr.id=$1`, id))
}

func (s *Server) portalCreateGoalReward(ctx context.Context, in portalGoalRewardInput) (*portalGoalRewardDTO, error) {
	rewardType := 0
	if in.RewardType != nil {
		rewardType = *in.RewardType
	}
	var id int64
	err := s.portalDB.QueryRow(ctx, `INSERT INTO goals_rewards
		(goal_id, badge_id, course_id, points, created_at, end_date_target, reward_type, start_date_target, points_target)
		SELECT $1,$2,$3,$4,NOW(),$5,$6,$7,$8
		FROM goals g, badge b, course c WHERE g.id=$1 AND b.id=$2 AND c.id=$3
		RETURNING id`,
		*in.GoalID, *in.BadgeID, *in.CourseID, in.Points, in.EndDateTarget, rewardType, in.StartDateTarget, in.PointsTarget).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundErr("Meta, medalha ou curso")
	}
	if err != nil {
		return nil, portalDBErr(err)
	}
	return s.portalGetReward(ctx, id)
}

func (s *Server) portalUpdateGoalReward(ctx context.Context, id int64, in portalGoalRewardInput) (*portalGoalRewardDTO, error) {
	// COALESCE preserva os valores atuais nos campos não enviados. Para os
	// alvos de data e pontos (que podem ser legitimamente NULL), COALESCE só
	// substitui quando o input traz valor — manter null no input = manter atual.
	tag, err := s.portalDB.Exec(ctx, `UPDATE goals_rewards gr SET
		goal_id = COALESCE($2, goal_id),
		badge_id = COALESCE($3, badge_id),
		course_id = COALESCE($4, course_id),
		reward_type = COALESCE($5, reward_type),
		points = COALESCE($6, points),
		points_target = COALESCE($7, points_target),
		start_date_target = COALESCE($8, start_date_target),
		end_date_target = COALESCE($9, end_date_target)
		WHERE gr.id = $1
		  AND ($2::bigint IS NULL OR EXISTS (SELECT 1 FROM goals g WHERE g.id = $2))
		  AND ($3::bigint IS NULL OR EXISTS (SELECT 1 FROM badge b WHERE b.id = $3))
		  AND ($4::bigint IS NULL OR EXISTS (SELECT 1 FROM course c WHERE c.id = $4))`,
		id, in.GoalID, in.BadgeID, in.CourseID, in.RewardType, in.Points, in.PointsTarget, in.StartDateTarget, in.EndDateTarget)
	if err != nil {
		return nil, portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		// inexistente OU FK inválida; um GET decide qual mensagem.
		if _, err := s.portalGetReward(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return nil, notFoundErr("Recompensa")
		}
		return nil, notFoundErr("Meta, medalha ou curso")
	}
	return s.portalGetReward(ctx, id)
}

// portalDeleteGoalReward remove a recompensa e suas atribuições (goals_students).
func (s *Server) portalDeleteGoalReward(ctx context.Context, id int64) error {
	tx, err := s.portalDB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM goals_rewards WHERE id=$1 FOR UPDATE)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return notFoundErr("Recompensa")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM goals_students WHERE goal_reward_id=$1`, id); err != nil {
		return portalDBErr(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM goals_rewards WHERE id=$1`, id); err != nil {
		return portalDBErr(err)
	}
	return tx.Commit(ctx)
}

// ── Goal students (progresso/atribuição) ─────────────────────────────────────

const portalGoalStudentCols = `gs.id::text, gs.user_id::text, gs.goal_reward_id::text, gs.course_id::text,
	gs.progress::text, COALESCE(gs.is_completed,false), gs.completed_at, COALESCE(gs.reward_claimed,false), gs.reward_claimed_at,
	COALESCE(u.name,''), COALESCE(u.email,''),
	COALESCE(g.name,''), g.description, g.image_url, g.type,
	b.id::text, b.name, b.icon_url, c.name, gr.reward_type, gr.points::text, gr.points_target::text, gr.start_date_target, gr.end_date_target`

const portalGoalStudentJoins = `FROM goals_students gs
	JOIN "user" u ON u.id = gs.user_id
	JOIN goals_rewards gr ON gr.id = gs.goal_reward_id
	JOIN goals g ON g.id = gr.goal_id
	JOIN badge b ON b.id = gr.badge_id
	JOIN course c ON c.id = gs.course_id`

func scanGoalStudent(row pgx.Row) (*portalGoalStudentDTO, error) {
	var d portalGoalStudentDTO
	var progress string
	var points, pointsTarget *string
	if err := row.Scan(&d.ID, &d.UserID, &d.GoalRewardID, &d.CourseID, &progress, &d.IsCompleted, &d.CompletedAt,
		&d.RewardClaimed, &d.RewardClaimedAt, &d.User.Nome, &d.User.Email,
		&d.Goal.Name, &d.Goal.Description, &d.Goal.ImageURL, &d.Goal.Type,
		&d.Reward.BadgeID, &d.Reward.BadgeName, &d.Reward.BadgeIconURL, &d.Reward.CourseName,
		&d.Reward.RewardType, &points, &pointsTarget, &d.Reward.StartDateTarget, &d.Reward.EndDateTarget); err != nil {
		return nil, err
	}
	d.User.ID = d.UserID
	if v := parseNumericText(&progress); v != nil {
		d.Progress = *v
	}
	d.Reward.Points = parseNumericText(points)
	d.Reward.PointsTarget = parseNumericText(pointsTarget)
	return &d, nil
}

func (s *Server) portalGetGoalStudent(ctx context.Context, id int64) (*portalGoalStudentDTO, error) {
	return scanGoalStudent(s.portalDB.QueryRow(ctx, `SELECT `+portalGoalStudentCols+` `+portalGoalStudentJoins+` WHERE gs.id=$1`, id))
}

func (s *Server) portalListGoalStudents(ctx context.Context, courseID, goalRewardID, userID *int64, p portalPagination) ([]portalGoalStudentDTO, int64, error) {
	where := `WHERE ($1::bigint IS NULL OR gs.course_id = $1)
		AND ($2::bigint IS NULL OR gs.goal_reward_id = $2)
		AND ($3::bigint IS NULL OR gs.user_id = $3)`
	var total int64
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM goals_students gs `+where, courseID, goalRewardID, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.portalDB.Query(ctx, `SELECT `+portalGoalStudentCols+` `+portalGoalStudentJoins+` `+where+`
		ORDER BY gs.is_completed ASC, gs.progress DESC, gs.id DESC LIMIT $4 OFFSET $5`,
		courseID, goalRewardID, userID, p.Limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalGoalStudentDTO{}
	for rows.Next() {
		d, err := scanGoalStudent(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *d)
	}
	return items, total, rows.Err()
}

func (s *Server) portalCreateGoalStudent(ctx context.Context, in portalGoalStudentCreateInput) (*portalGoalStudentDTO, error) {
	var dup bool
	if err := s.portalDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM goals_students WHERE user_id=$1 AND goal_reward_id=$2)`, in.UserID, in.GoalRewardID).Scan(&dup); err != nil {
		return nil, err
	}
	if dup {
		return nil, conflictErr("aluno já atribuído a essa meta")
	}
	var id int64
	err := s.portalDB.QueryRow(ctx, `INSERT INTO goals_students
		(user_id, goal_reward_id, course_id, progress, is_completed, completed_at, reward_claimed, reward_claimed_at)
		SELECT $1,$2,$3,0,false,NULL,false,NULL
		FROM "user" u, goals_rewards gr, course c WHERE u.id=$1 AND gr.id=$2 AND c.id=$3
		RETURNING id`, in.UserID, in.GoalRewardID, in.CourseID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFoundErr("Aluno, recompensa ou curso")
	}
	if err != nil {
		return nil, portalDBErr(err)
	}
	return s.portalGetGoalStudent(ctx, id)
}

func (s *Server) portalUpdateGoalStudent(ctx context.Context, id int64, in portalGoalStudentUpdateInput) (*portalGoalStudentDTO, error) {
	// checa duplicidade se trocar de recompensa
	if in.GoalRewardID != nil {
		var userID, curReward int64
		err := s.portalDB.QueryRow(ctx, `SELECT user_id, goal_reward_id FROM goals_students WHERE id=$1`, id).Scan(&userID, &curReward)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFoundErr("Registro de progresso")
		}
		if err != nil {
			return nil, err
		}
		if *in.GoalRewardID != curReward {
			var dup bool
			if err := s.portalDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM goals_students WHERE user_id=$1 AND goal_reward_id=$2 AND id<>$3)`, userID, *in.GoalRewardID, id).Scan(&dup); err != nil {
				return nil, err
			}
			if dup {
				return nil, conflictErr("aluno já atribuído a essa meta")
			}
		}
	}
	tag, err := s.portalDB.Exec(ctx, `UPDATE goals_students gs SET
		progress = COALESCE($2, progress),
		is_completed = COALESCE($3, is_completed),
		goal_reward_id = COALESCE($4, goal_reward_id),
		course_id = COALESCE($5, course_id),
		completed_at = CASE
			WHEN COALESCE($3, gs.is_completed) = true AND gs.completed_at IS NULL THEN NOW()
			WHEN $3 = false THEN NULL
			ELSE gs.completed_at
		END
		WHERE gs.id = $1`, id, in.Progress, in.IsCompleted, in.GoalRewardID, in.CourseID)
	if err != nil {
		return nil, portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, notFoundErr("Registro de progresso")
	}
	return s.portalGetGoalStudent(ctx, id)
}

func (s *Server) portalDeleteGoalStudent(ctx context.Context, id int64) error {
	tag, err := s.portalDB.Exec(ctx, `DELETE FROM goals_students WHERE id=$1`, id)
	if err != nil {
		return portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr("Registro de progresso")
	}
	return nil
}

// portalClaimGoalReward marca a recompensa como resgatada; só funciona se a meta
// está concluída e ainda não foi resgatada. userScope != nil restringe ao próprio
// aluno (autosserviço); nil = staff resgatando em nome de alguém.
func (s *Server) portalClaimGoalReward(ctx context.Context, id int64, userScope *int64) (*portalGoalStudentDTO, error) {
	tag, err := s.portalDB.Exec(ctx, `UPDATE goals_students gs
		SET reward_claimed = true, reward_claimed_at = NOW()
		WHERE gs.id = $1 AND gs.is_completed = true AND gs.reward_claimed = false
		  AND ($2::bigint IS NULL OR gs.user_id = $2)`, id, userScope)
	if err != nil {
		return nil, portalDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, validationErr("recompensa indisponível para resgate")
	}
	return s.portalGetGoalStudent(ctx, id)
}
