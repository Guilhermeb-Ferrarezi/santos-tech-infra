package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// portalOptionalID lê um filtro de id da query (?name=123); vazio → nil.
func portalOptionalID(r *http.Request, name string) (*int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, validationErr(name + " inválido")
	}
	return &id, nil
}

// handlePortalSearchUsers — GET /portal/users?q=&limit= : seletor de alunos.
func (s *Server) handlePortalSearchUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit := atoiMin(r.URL.Query().Get("limit"), 20, 1)
	if limit > 50 {
		limit = 50
	}
	items, err := s.portalSearchUsers(r.Context(), q, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ── Badges ───────────────────────────────────────────────────────────────────

func (s *Server) handlePortalListBadges(w http.ResponseWriter, r *http.Request) {
	p := portalPaginationFrom(r)
	items, total, err := s.portalListBadges(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

func (s *Server) handlePortalCreateBadge(w http.ResponseWriter, r *http.Request) {
	var in portalBadgeInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateCreate(); err != nil {
		writeErr(w, err)
		return
	}
	b, err := s.portalCreateBadge(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "badge_create", "badge", b.ID, nil)
	writeJSON(w, http.StatusCreated, map[string]any{"badge": b})
}

func (s *Server) handlePortalUpdateBadge(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "badgeId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalBadgeInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateUpdate(); err != nil {
		writeErr(w, err)
		return
	}
	b, err := s.portalUpdateBadge(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "badge_update", "badge", b.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"badge": b})
}

func (s *Server) handlePortalDeleteBadge(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "badgeId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalDeleteBadge(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "badge_delete", "badge", fmt.Sprint(id), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePortalListHolders(w http.ResponseWriter, r *http.Request) {
	badgeID, err := portalOptionalID(r, "badgeId")
	if err != nil {
		writeErr(w, err)
		return
	}
	p := portalPaginationFrom(r)
	items, total, err := s.portalListHolders(r.Context(), badgeID, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

func (s *Server) handlePortalAssignBadge(w http.ResponseWriter, r *http.Request) {
	var in portalBadgeAssignInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	h, err := s.portalAssignBadge(r.Context(), in.UserID, in.BadgeID)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "badge_holder_create", "badge", h.HolderID, map[string]any{"badgeId": in.BadgeID, "userId": in.UserID})
	writeJSON(w, http.StatusCreated, map[string]any{"holder": h})
}

func (s *Server) handlePortalUpdateHolder(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "holderId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalBadgeHolderUpdateInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	h, err := s.portalUpdateHolderBadge(r.Context(), id, in.BadgeID)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "badge_holder_update", "badge", h.HolderID, map[string]any{"badgeId": in.BadgeID})
	writeJSON(w, http.StatusOK, map[string]any{"holder": h})
}

func (s *Server) handlePortalDeleteHolder(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "holderId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalDeleteHolder(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "badge_holder_delete", "badge", fmt.Sprint(id), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── Goals ────────────────────────────────────────────────────────────────────

func (s *Server) handlePortalListGoals(w http.ResponseWriter, r *http.Request) {
	p := portalPaginationFrom(r)
	items, total, err := s.portalListGoals(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

func (s *Server) handlePortalGetGoal(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "goalId")
	if err != nil {
		writeErr(w, err)
		return
	}
	g, rewards, err := s.portalGetGoalWithRewards(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, notFoundErr("Meta"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"goal": g, "rewards": rewards})
}

func (s *Server) handlePortalCreateGoal(w http.ResponseWriter, r *http.Request) {
	var in portalGoalInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateCreate(); err != nil {
		writeErr(w, err)
		return
	}
	g, err := s.portalCreateGoal(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "goal_create", "goal", g.ID, nil)
	writeJSON(w, http.StatusCreated, map[string]any{"goal": g})
}

func (s *Server) handlePortalUpdateGoal(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "goalId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalGoalInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateUpdate(); err != nil {
		writeErr(w, err)
		return
	}
	g, err := s.portalUpdateGoal(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "goal_update", "goal", g.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"goal": g})
}

func (s *Server) handlePortalDeleteGoal(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "goalId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalDeleteGoal(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "goal_delete", "goal", fmt.Sprint(id), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── Goal rewards ─────────────────────────────────────────────────────────────

func (s *Server) handlePortalListGoalRewards(w http.ResponseWriter, r *http.Request) {
	goalID, err := portalOptionalID(r, "goalId")
	if err != nil {
		writeErr(w, err)
		return
	}
	courseID, err := portalOptionalID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	p := portalPaginationFrom(r)
	items, err := s.portalListGoalRewards(r.Context(), goalID, courseID, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	total, err := s.portalCountGoalRewards(r.Context(), goalID, courseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

func (s *Server) handlePortalCreateGoalReward(w http.ResponseWriter, r *http.Request) {
	var in portalGoalRewardInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateCreate(); err != nil {
		writeErr(w, err)
		return
	}
	reward, err := s.portalCreateGoalReward(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "goal_reward_create", "goal_reward", reward.ID, map[string]any{"goalId": *in.GoalID, "badgeId": *in.BadgeID, "courseId": *in.CourseID})
	writeJSON(w, http.StatusCreated, map[string]any{"reward": reward})
}

func (s *Server) handlePortalUpdateGoalReward(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "rewardId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalGoalRewardInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateUpdate(); err != nil {
		writeErr(w, err)
		return
	}
	reward, err := s.portalUpdateGoalReward(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "goal_reward_update", "goal_reward", reward.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"reward": reward})
}

func (s *Server) handlePortalDeleteGoalReward(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "rewardId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalDeleteGoalReward(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "goal_reward_delete", "goal_reward", fmt.Sprint(id), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── Goal students ────────────────────────────────────────────────────────────

func (s *Server) handlePortalListGoalStudents(w http.ResponseWriter, r *http.Request) {
	courseID, err := portalOptionalID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	goalRewardID, err := portalOptionalID(r, "goalRewardId")
	if err != nil {
		writeErr(w, err)
		return
	}
	p := portalPaginationFrom(r)
	items, total, err := s.portalListGoalStudents(r.Context(), courseID, goalRewardID, nil, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

func (s *Server) handlePortalCreateGoalStudent(w http.ResponseWriter, r *http.Request) {
	var in portalGoalStudentCreateInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	gs, err := s.portalCreateGoalStudent(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "goal_student_create", "goal_student", gs.ID, map[string]any{"userId": in.UserID, "goalRewardId": in.GoalRewardID, "courseId": in.CourseID})
	writeJSON(w, http.StatusCreated, map[string]any{"goalStudent": gs})
}

func (s *Server) handlePortalUpdateGoalStudent(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "goalStudentId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalGoalStudentUpdateInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	gs, err := s.portalUpdateGoalStudent(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "goal_student_update", "goal_student", gs.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"goalStudent": gs})
}

func (s *Server) handlePortalDeleteGoalStudent(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "goalStudentId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalDeleteGoalStudent(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "goal_student_delete", "goal_student", fmt.Sprint(id), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePortalClaimGoalReward(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "goalStudentId")
	if err != nil {
		writeErr(w, err)
		return
	}
	// staff resgata em nome de qualquer aluno; aluno só o próprio registro.
	var scope *int64
	if u, err := s.userByID(r.Context(), userIDFrom(r)); err == nil && u != nil && u.Role == RoleStudent {
		uid := u.ID
		scope = &uid
	}
	gs, err := s.portalClaimGoalReward(r.Context(), id, scope)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "goal_student_claim", "goal_student", gs.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"goalStudent": gs})
}
