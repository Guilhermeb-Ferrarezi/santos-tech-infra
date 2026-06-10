package main

import (
	"strings"
	"time"
)

// ── Badges (medalhas) ────────────────────────────────────────────────────────

type portalBadgeDTO struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	IconURL      string    `json:"iconUrl"`
	CreatedAt    time.Time `json:"createdAt"`
	HoldersCount int64     `json:"holdersCount"`
}

type portalBadgeHolderUserDTO struct {
	ID    string `json:"id"`
	Nome  string `json:"nome"`
	Email string `json:"email"`
}

type portalBadgeHolderDTO struct {
	HolderID  string                   `json:"holderId"`
	BadgeID   string                   `json:"badgeId"`
	BadgeName string                   `json:"badgeName"`
	User      portalBadgeHolderUserDTO `json:"user"`
	AwardedAt time.Time                `json:"awardedAt"`
}

// portalBadgeInput é o corpo de criação/edição. O ícone chega como URL já
// hospedada (upload prévio via /auth/upload), não base64 — segue o padrão de
// upload das Fases 1-3.
type portalBadgeInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	IconURL     *string `json:"iconUrl"`
}

func (in *portalBadgeInput) validateCreate() error {
	if in.Name == nil || len(strings.TrimSpace(*in.Name)) < 2 {
		return validationErr("nome obrigatório")
	}
	if in.Description == nil || len(strings.TrimSpace(*in.Description)) < 2 {
		return validationErr("descrição obrigatória")
	}
	if in.IconURL == nil || strings.TrimSpace(*in.IconURL) == "" {
		return validationErr("ícone obrigatório")
	}
	return nil
}

func (in *portalBadgeInput) validateUpdate() error {
	if in.Name == nil && in.Description == nil && in.IconURL == nil {
		return validationErr("informe ao menos um campo para atualização")
	}
	if in.Name != nil && len(strings.TrimSpace(*in.Name)) < 2 {
		return validationErr("nome obrigatório")
	}
	if in.Description != nil && len(strings.TrimSpace(*in.Description)) < 2 {
		return validationErr("descrição obrigatória")
	}
	return nil
}

type portalBadgeAssignInput struct {
	UserID  int64 `json:"userId"`
	BadgeID int64 `json:"badgeId"`
}

func (in *portalBadgeAssignInput) validate() error {
	if in.UserID <= 0 {
		return validationErr("userId inválido")
	}
	if in.BadgeID <= 0 {
		return validationErr("badgeId inválido")
	}
	return nil
}

type portalBadgeHolderUpdateInput struct {
	BadgeID int64 `json:"badgeId"`
}

func (in *portalBadgeHolderUpdateInput) validate() error {
	if in.BadgeID <= 0 {
		return validationErr("badgeId inválido")
	}
	return nil
}

// ── Goals (metas) ────────────────────────────────────────────────────────────

type portalGoalDTO struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	Type         *int    `json:"type"`
	ImageURL     *string `json:"imageUrl"`
	RewardsCount int64   `json:"rewardsCount"`
}

type portalGoalInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Type        *int    `json:"type"`
	ImageURL    *string `json:"imageUrl"`
}

// portalGoalTypeValid valida o enum 1..5 da meta.
func portalGoalTypeValid(t int) bool { return t >= 1 && t <= 5 }

func (in *portalGoalInput) validateCreate() error {
	if in.Name == nil || len(strings.TrimSpace(*in.Name)) < 2 {
		return validationErr("nome obrigatório")
	}
	if in.Type != nil && !portalGoalTypeValid(*in.Type) {
		return validationErr("tipo de meta inválido (use 1 a 5)")
	}
	return nil
}

func (in *portalGoalInput) validateUpdate() error {
	if in.Name == nil && in.Description == nil && in.Type == nil && in.ImageURL == nil {
		return validationErr("informe ao menos um campo para atualização")
	}
	if in.Name != nil && len(strings.TrimSpace(*in.Name)) < 2 {
		return validationErr("nome obrigatório")
	}
	if in.Type != nil && !portalGoalTypeValid(*in.Type) {
		return validationErr("tipo de meta inválido (use 1 a 5)")
	}
	return nil
}

type portalGoalRewardDTO struct {
	ID              string     `json:"id"`
	GoalID          string     `json:"goalId"`
	BadgeID         string     `json:"badgeId"`
	CourseID        string     `json:"courseId"`
	Points          *float64   `json:"points"`
	CreatedAt       time.Time  `json:"createdAt"`
	EndDateTarget   *time.Time `json:"endDateTarget"`
	RewardType      int        `json:"rewardType"`
	StartDateTarget *time.Time `json:"startDateTarget"`
	PointsTarget    *float64   `json:"pointsTarget"`
	GoalName        string     `json:"goalName"`
	BadgeName       string     `json:"badgeName"`
	BadgeIconURL    *string    `json:"badgeIconUrl"`
	CourseName      string     `json:"courseName"`
}

type portalGoalRewardInput struct {
	GoalID          *int64     `json:"goalId"`
	BadgeID         *int64     `json:"badgeId"`
	CourseID        *int64     `json:"courseId"`
	RewardType      *int       `json:"rewardType"`
	PointsTarget    *float64   `json:"pointsTarget"`
	StartDateTarget *time.Time `json:"startDateTarget"`
	EndDateTarget   *time.Time `json:"endDateTarget"`
	Points          *float64   `json:"points"`
}

func (in *portalGoalRewardInput) validateCreate() error {
	if in.GoalID == nil || *in.GoalID <= 0 {
		return validationErr("goalId inválido")
	}
	if in.BadgeID == nil || *in.BadgeID <= 0 {
		return validationErr("badgeId inválido")
	}
	if in.CourseID == nil || *in.CourseID <= 0 {
		return validationErr("courseId inválido")
	}
	return nil
}

func (in *portalGoalRewardInput) validateUpdate() error {
	if in.GoalID == nil && in.BadgeID == nil && in.CourseID == nil && in.RewardType == nil &&
		in.PointsTarget == nil && in.StartDateTarget == nil && in.EndDateTarget == nil && in.Points == nil {
		return validationErr("informe ao menos um campo para atualização")
	}
	if in.GoalID != nil && *in.GoalID <= 0 {
		return validationErr("goalId inválido")
	}
	if in.BadgeID != nil && *in.BadgeID <= 0 {
		return validationErr("badgeId inválido")
	}
	if in.CourseID != nil && *in.CourseID <= 0 {
		return validationErr("courseId inválido")
	}
	return nil
}

type portalGoalStudentUserDTO struct {
	ID    string `json:"id"`
	Nome  string `json:"nome"`
	Email string `json:"email"`
}

type portalGoalStudentGoalDTO struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	ImageURL    *string `json:"imageUrl"`
	Type        *int    `json:"type"`
}

type portalGoalStudentRewardDTO struct {
	BadgeID         *string    `json:"badgeId"`
	BadgeName       *string    `json:"badgeName"`
	BadgeIconURL    *string    `json:"badgeIconUrl"`
	CourseName      *string    `json:"courseName"`
	RewardType      *int       `json:"rewardType"`
	Points          *float64   `json:"points"`
	PointsTarget    *float64   `json:"pointsTarget"`
	StartDateTarget *time.Time `json:"startDateTarget"`
	EndDateTarget   *time.Time `json:"endDateTarget"`
}

type portalGoalStudentDTO struct {
	ID              string                     `json:"id"`
	UserID          string                     `json:"userId"`
	GoalRewardID    string                     `json:"goalRewardId"`
	CourseID        string                     `json:"courseId"`
	Progress        float64                    `json:"progress"`
	IsCompleted     bool                       `json:"isCompleted"`
	CompletedAt     *time.Time                 `json:"completedAt"`
	RewardClaimed   bool                       `json:"rewardClaimed"`
	RewardClaimedAt *time.Time                 `json:"rewardClaimedAt"`
	User            portalGoalStudentUserDTO   `json:"user"`
	Goal            portalGoalStudentGoalDTO   `json:"goal"`
	Reward          portalGoalStudentRewardDTO `json:"reward"`
}

type portalGoalStudentCreateInput struct {
	UserID       int64 `json:"userId"`
	GoalRewardID int64 `json:"goalRewardId"`
	CourseID     int64 `json:"courseId"`
}

func (in *portalGoalStudentCreateInput) validate() error {
	if in.UserID <= 0 {
		return validationErr("userId inválido")
	}
	if in.GoalRewardID <= 0 {
		return validationErr("goalRewardId inválido")
	}
	if in.CourseID <= 0 {
		return validationErr("courseId inválido")
	}
	return nil
}

type portalGoalStudentUpdateInput struct {
	Progress     *float64 `json:"progress"`
	IsCompleted  *bool    `json:"isCompleted"`
	GoalRewardID *int64   `json:"goalRewardId"`
	CourseID     *int64   `json:"courseId"`
}

func (in *portalGoalStudentUpdateInput) validate() error {
	if in.Progress == nil && in.IsCompleted == nil && in.GoalRewardID == nil && in.CourseID == nil {
		return validationErr("informe ao menos um campo para atualização")
	}
	if in.Progress != nil && *in.Progress < 0 {
		return validationErr("progress não pode ser negativo")
	}
	if in.GoalRewardID != nil && *in.GoalRewardID <= 0 {
		return validationErr("goalRewardId inválido")
	}
	if in.CourseID != nil && *in.CourseID <= 0 {
		return validationErr("courseId inválido")
	}
	return nil
}
