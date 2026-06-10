package main

import "time"

// ── Respostas (answer) e correção ────────────────────────────────────────────

type portalAnswerStudentDTO struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type portalAnswerDTO struct {
	ID             string                 `json:"id"`
	Student        portalAnswerStudentDTO `json:"student"`
	QuestionID     string                 `json:"questionId"`
	Question       string                 `json:"question"`
	Options        []portalOptionDTO      `json:"options"`
	AnswerText     *string                `json:"answerText"`
	SelectedOption *string                `json:"selectedOption"`
	IsCorrect      *bool                  `json:"isCorrect"`
	Feedback       *string                `json:"feedback"`
	AnsweredAt     *time.Time             `json:"answeredAt"`
}

type portalAnswerStats struct {
	Total       int64 `json:"total"`
	Corrigidas  int64 `json:"corrigidas"`
	Pendentes   int64 `json:"pendentes"`
	Corretas    int64 `json:"corretas"`
	Incorretas  int64 `json:"incorretas"`
	TotalAlunos int64 `json:"totalAlunos"`
}

// portalAnswerStudentSummaryDTO — aluno + contagem de respostas (visões de
// "quem respondeu").
type portalAnswerStudentSummaryDTO struct {
	StudentID      string     `json:"studentId"`
	Name           string     `json:"name"`
	Email          string     `json:"email"`
	TotalAnswers   int64      `json:"totalAnswers"`
	LastAnsweredAt *time.Time `json:"lastAnsweredAt"`
}

// portalStudentExerciseDTO — exercício respondido por um aluno.
type portalStudentExerciseDTO struct {
	ExerciseID     string     `json:"exerciseId"`
	Title          string     `json:"title"`
	Module         *string    `json:"module"`
	Theme          *string    `json:"theme"`
	TotalAnswers   int64      `json:"totalAnswers"`
	LastAnsweredAt *time.Time `json:"lastAnsweredAt"`
}

// filtros da listagem rica de respostas de um exercício.
type portalAnswerFilter struct {
	Status    string // "todos" | "corrigida" | "pendente"
	StudentID int64  // 0 = todos
}

// portalAnswerPatch — correção/edição parcial de uma resposta.
type portalAnswerPatch struct {
	AnswerText     *string `json:"answerText"`
	SelectedOption *int64  `json:"selectedOption"`
	IsCorrect      *bool   `json:"isCorrect"`
	Feedback       *string `json:"feedback"`
}

func (p *portalAnswerPatch) validate() error {
	if p.AnswerText == nil && p.SelectedOption == nil && p.IsCorrect == nil && p.Feedback == nil {
		return validationErr("envie ao menos um campo para atualizar")
	}
	return nil
}

type portalAnswerBatchInput struct {
	AnswerIDs []int64           `json:"answerIds"`
	Patch     portalAnswerPatch `json:"patch"`
}

func (in *portalAnswerBatchInput) validate() error {
	if len(in.AnswerIDs) == 0 {
		return validationErr("answerIds obrigatório")
	}
	if len(in.AnswerIDs) > 500 {
		return validationErr("no máximo 500 respostas por lote")
	}
	return in.Patch.validate()
}

// ── Progresso por fase ───────────────────────────────────────────────────────

type portalProgressDTO struct {
	StudentID   string     `json:"studentId"`
	Name        string     `json:"name"`
	Email       string     `json:"email"`
	PhaseID     string     `json:"phaseId"`
	PhaseName   *string    `json:"phaseName,omitempty"`
	Status      int        `json:"status"`
	StatusLabel string     `json:"statusLabel"`
	Progress    float64    `json:"progress"`
	UnlockedAt  *time.Time `json:"unlockedAt"`
	CompletedAt *time.Time `json:"completedAt"`
}

// portalProgressStatus normaliza o status: 0=não iniciado, 1=em progresso,
// 2=concluído. status NULL com unlocked_at preenchido conta como em progresso.
func portalProgressStatus(rawStatus *int, unlockedAt *time.Time) (int, string) {
	s := 0
	if rawStatus != nil {
		s = *rawStatus
	}
	if s == 0 && unlockedAt != nil {
		s = 1
	}
	switch s {
	case 2:
		return 2, "concluido"
	case 1:
		return 1, "em_progresso"
	default:
		return 0, "nao_iniciado"
	}
}
