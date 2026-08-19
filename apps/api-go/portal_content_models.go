package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ── Exercícios e questões ────────────────────────────────────────────────────

// type_exercise: 0=código, 1=múltipla escolha, 2=texto/escrita.
type portalOptionDTO struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	IsCorrect bool   `json:"isCorrect"`
}

type portalQuestionDTO struct {
	ID        string            `json:"id"`
	Statement string            `json:"statement"`
	Options   []portalOptionDTO `json:"options"`
}

type portalExerciseDTO struct {
	ID              string              `json:"id"`
	PhaseID         string              `json:"phaseId"`
	Title           string              `json:"title"`
	Description     string              `json:"description"`
	Type            int                 `json:"type"`
	Difficulty      int                 `json:"difficulty"`
	IndexOrder      int                 `json:"indexOrder"`
	IsDailyTask     bool                `json:"isDailyTask"`
	IsFinalExercise bool                `json:"isFinalExercise"`
	PointsRedeem    int                 `json:"pointsRedeem"`
	VideoURL        *string             `json:"videoUrl"`
	TermAt          *time.Time          `json:"termAt"`
	CreatedAt       time.Time           `json:"createdAt"`
	UpdatedAt       time.Time           `json:"updatedAt"`
	Questions       []portalQuestionDTO `json:"questions,omitempty"`
}

type portalOptionInput struct {
	Text      string `json:"text"`
	IsCorrect bool   `json:"isCorrect"`
}

type portalQuestionInput struct {
	Statement string              `json:"statement"`
	Options   []portalOptionInput `json:"options"`
}

type portalExerciseInput struct {
	Title           string  `json:"title"`
	Description     string  `json:"description"`
	Type            *int    `json:"type"`
	Difficulty      *int    `json:"difficulty"`
	IndexOrder      *int    `json:"indexOrder"`
	IsDailyTask     *bool   `json:"isDailyTask"`
	IsFinalExercise *bool   `json:"isFinalExercise"`
	PointsRedeem    *int    `json:"pointsRedeem"`
	VideoURL        *string `json:"videoUrl"`
	TermAt          *string `json:"termAt"`
	// PhaseID: só em PATCH — realoca o exercício pra outra fase (não usado no
	// create, que já recebe a fase via path /portal/phases/{phaseId}/exercises).
	PhaseID   *int64                `json:"phaseId"`
	Questions []portalQuestionInput `json:"questions"`
}

func (in *portalExerciseInput) validateCreate() error {
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	if len(in.Title) < 2 {
		return validationErr("title obrigatório")
	}
	if len(in.Description) < 2 {
		return validationErr("description obrigatório")
	}
	if in.Type != nil && (*in.Type < 0 || *in.Type > 2) {
		return validationErr("type deve ser 0 (código), 1 (múltipla) ou 2 (texto)")
	}
	if in.Difficulty != nil && *in.Difficulty < 1 {
		return validationErr("difficulty deve ser >= 1")
	}
	if in.IndexOrder != nil && *in.IndexOrder < 1 {
		return validationErr("indexOrder deve ser >= 1")
	}
	if in.PointsRedeem != nil && *in.PointsRedeem < 0 {
		return validationErr("pointsRedeem deve ser >= 0")
	}
	return in.validateQuestions()
}

// validateQuestions exige, para múltipla escolha, ao menos uma questão; e valida
// a forma de toda questão fornecida.
func (in *portalExerciseInput) validateQuestions() error {
	isMultipla := in.Type != nil && *in.Type == 1
	if isMultipla && len(in.Questions) == 0 {
		return validationErr("múltipla escolha exige ao menos uma questão")
	}
	return validateQuestionShapes(in.Questions)
}

// validateQuestionShapes valida cada questão: enunciado, >=2 opções com texto e
// exatamente uma correta. Lista vazia é válida (sem alteração de questões).
func validateQuestionShapes(qs []portalQuestionInput) error {
	for _, q := range qs {
		if strings.TrimSpace(q.Statement) == "" {
			return validationErr("questão sem enunciado")
		}
		if len(q.Options) < 2 {
			return validationErr("cada questão precisa de ao menos 2 opções")
		}
		correct := 0
		for _, o := range q.Options {
			if strings.TrimSpace(o.Text) == "" {
				return validationErr("opção sem texto")
			}
			if o.IsCorrect {
				correct++
			}
		}
		if correct != 1 {
			return validationErr("cada questão precisa de exatamente uma opção correta")
		}
	}
	return nil
}

// validateUpdate valida apenas o que veio (PATCH parcial): ranges numéricos e a
// forma das questões, sem exigir title/description.
func (in *portalExerciseInput) validateUpdate() error {
	if in.Type != nil && (*in.Type < 0 || *in.Type > 2) {
		return validationErr("type deve ser 0 (código), 1 (múltipla) ou 2 (texto)")
	}
	// converter para múltipla escolha exige enviar as questões no mesmo PATCH,
	// senão o exercício ficaria type=1 sem nenhuma questão.
	if in.Type != nil && *in.Type == 1 && len(in.Questions) == 0 {
		return validationErr("múltipla escolha exige ao menos uma questão")
	}
	if in.Difficulty != nil && *in.Difficulty < 1 {
		return validationErr("difficulty deve ser >= 1")
	}
	if in.IndexOrder != nil && *in.IndexOrder < 1 {
		return validationErr("indexOrder deve ser >= 1")
	}
	if in.PointsRedeem != nil && *in.PointsRedeem < 0 {
		return validationErr("pointsRedeem deve ser >= 0")
	}
	if in.PhaseID != nil && *in.PhaseID <= 0 {
		return validationErr("phaseId inválido")
	}
	return validateQuestionShapes(in.Questions)
}

// ── Containers (agrupadores de exercícios) ───────────────────────────────────

type portalContainerExerciseDTO struct {
	ContainerTaskID string `json:"containerTaskId"`
	ExerciseID      string `json:"exerciseId"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	IndexOrder      int    `json:"indexOrder"`
}

type portalContainerGroupDTO struct {
	Name                   string                       `json:"name"`
	PhaseID                string                       `json:"phaseId"`
	IsDailyTask            bool                         `json:"isDailyTask"`
	ContainerDateTargetInt *int                         `json:"containerDateTargetInt"`
	Exercises              []portalContainerExerciseDTO `json:"exercises"`
}

type portalContainerInput struct {
	Name                   string  `json:"name"`
	PhaseID                int64   `json:"phaseId"`
	ExerciseIDs            []int64 `json:"exerciseIds"`
	IsDailyTask            *bool   `json:"isDailyTask"`
	ContainerDateTargetInt *int    `json:"containerDateTargetInt"`
}

func (in *portalContainerInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return validationErr("name obrigatório")
	}
	if in.PhaseID <= 0 {
		return validationErr("phaseId obrigatório")
	}
	return portalValidateIDs("exerciseIds", in.ExerciseIDs)
}

type portalContainerGroupRef struct {
	Name                   string `json:"name"`
	PhaseID                int64  `json:"phaseId"`
	IsDailyTask            bool   `json:"isDailyTask"`
	ContainerDateTargetInt *int   `json:"containerDateTargetInt"`
}

func (in *portalContainerGroupRef) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return validationErr("name obrigatório")
	}
	if in.PhaseID <= 0 {
		return validationErr("phaseId obrigatório")
	}
	return nil
}

type portalAddContainerInput struct {
	Name                   string  `json:"name"`
	PhaseID                int64   `json:"phaseId"`
	IsDailyTask            bool    `json:"isDailyTask"`
	ContainerDateTargetInt *int    `json:"containerDateTargetInt"`
	ExerciseIDs            []int64 `json:"exerciseIds"`
}

func (in *portalAddContainerInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return validationErr("name obrigatório")
	}
	if in.PhaseID <= 0 {
		return validationErr("phaseId obrigatório")
	}
	return portalValidateIDs("exerciseIds", in.ExerciseIDs)
}

func (in *portalAddContainerInput) ref() portalContainerGroupRef {
	return portalContainerGroupRef{
		Name: in.Name, PhaseID: in.PhaseID, IsDailyTask: in.IsDailyTask, ContainerDateTargetInt: in.ContainerDateTargetInt,
	}
}

// ── Materiais e vídeos ───────────────────────────────────────────────────────

type portalMaterialDTO struct {
	ID          string    `json:"id"`
	CourseID    string    `json:"courseId"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	ModuleID    *string   `json:"moduleId"`
	Module      *string   `json:"module"`
	FileURL     *string   `json:"fileUrl"`
	FileType    *string   `json:"fileType"`
	CreatedAt   time.Time `json:"createdAt"`
}

type portalMaterialInput struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	ModuleID    *int64  `json:"moduleId"`
	Module      *string `json:"module"`
	FileURL     *string `json:"fileUrl"`
	FileType    *string `json:"fileType"`
}

func (in *portalMaterialInput) validateCreate() error {
	in.Title = strings.TrimSpace(in.Title)
	if len(in.Title) < 3 {
		return validationErr("title obrigatório (min 3)")
	}
	return nil
}

type portalVideoDTO struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	ModuleID        *string   `json:"moduleId"`
	Module          *string   `json:"module"`
	URL             string    `json:"url"`
	ThumbnailURL    *string   `json:"thumbnailUrl"`
	DurationSeconds *int      `json:"durationSeconds"`
	CreatedAt       time.Time `json:"createdAt"`
}

type portalVideoInput struct {
	Title           string  `json:"title"`
	Description     string  `json:"description"`
	ModuleID        *int64  `json:"moduleId"`
	Module          *string `json:"module"`
	URL             string  `json:"url"`
	ThumbnailURL    *string `json:"thumbnailUrl"`
	DurationSeconds *int    `json:"durationSeconds"`
}

func (in *portalVideoInput) validateCreate() error {
	in.Title = strings.TrimSpace(in.Title)
	if len(in.Title) < 3 {
		return validationErr("title obrigatório (min 3)")
	}
	in.URL = strings.TrimSpace(in.URL)
	if in.URL == "" {
		return validationErr("url obrigatório")
	}
	return nil
}

// ── Metadado de módulo embutido na description ───────────────────────────────
//
// A API antiga grava o módulo dentro de `description` como prefixo
// `[[module:<id>|<nome>]]`. Reproduzimos o encode/decode para não perder o
// vínculo de módulo nos dados existentes (material/video não têm FK de módulo).

// Consome o separador (uma quebra de linha opcional) após `]]`, preservando
// qualquer espaço em branco que faça parte do corpo da description.
var moduleMetaRegex = regexp.MustCompile(`^\[\[module:(\d+)\|([^\]]*)\]\]\n?`)

// parseModuleMeta separa o metadado de módulo do corpo da description.
func parseModuleMeta(raw string) (desc string, moduleID *string, moduleName *string) {
	m := moduleMetaRegex.FindStringSubmatch(raw)
	if m == nil {
		return raw, nil, nil
	}
	id := m[1]
	name := strings.TrimSpace(m[2])
	rest := strings.TrimPrefix(raw, m[0])
	var namePtr *string
	if name != "" {
		namePtr = &name
	}
	return rest, &id, namePtr
}

// composeModuleMeta embute o metadado de módulo no início da description.
func composeModuleMeta(desc string, moduleID *int64, moduleName *string) string {
	if moduleID == nil {
		return desc
	}
	name := ""
	if moduleName != nil {
		name = strings.TrimSpace(*moduleName)
	}
	prefix := "[[module:" + strconv.FormatInt(*moduleID, 10) + "|" + name + "]]"
	if desc == "" {
		return prefix
	}
	return prefix + "\n" + desc
}
