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

// portalAnswerPatch — CORREÇÃO de uma resposta. Só is_correct e feedback: o
// corretor avalia, não edita. answerText/selectedOption ficaram de fora de
// propósito — deixar o corretor reescrever a submissão do aluno (e ainda
// marcá-la como correta, disparando crédito de pontos) apaga a prova do que o
// aluno de fato respondeu. Como o decoder roda com DisallowUnknownFields, um
// cliente antigo que ainda mande esses campos recebe 400 em vez de um patch
// silenciosamente ignorado.
type portalAnswerPatch struct {
	IsCorrect *bool   `json:"isCorrect"`
	Feedback  *string `json:"feedback"`
}

func (p *portalAnswerPatch) validate() error {
	if p.IsCorrect == nil && p.Feedback == nil {
		return validationErr("envie ao menos um campo para atualizar (isCorrect e/ou feedback)")
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

// portalProgressDTO — sem e-mail: as telas de progresso identificam o aluno
// por id + nome, e a rota é alcançável por qualquer cargo com
// portal_correcao:read.
type portalProgressDTO struct {
	StudentID   string     `json:"studentId"`
	Name        string     `json:"name"`
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
