package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ── Paginação e parsers ──────────────────────────────────────────────────────

type portalPagination struct {
	Page   int
	Limit  int
	Offset int
	Query  string
}

// portalMaxPage — teto de página. Sem ele, ?page=9000000000000000 fazia
// (page-1)*limit estourar o int, o offset virava negativo e o Postgres
// devolvia erro de sintaxe → 500 em vez de uma página vazia.
const portalMaxPage = 100000

func portalPaginationFrom(r *http.Request) portalPagination {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	page := atoiMin(r.URL.Query().Get("page"), 1, 1)
	if page > portalMaxPage {
		page = portalMaxPage
	}
	limit := atoiMin(r.URL.Query().Get("limit"), 20, 1)
	if limit > 100 {
		limit = 100
	}
	offset := (page - 1) * limit
	if offset < 0 {
		offset = 0
	}
	return portalPagination{Page: page, Limit: limit, Offset: offset, Query: q}
}

// atoiMin lê um inteiro da query; vazio → fallback; inválido ou < min → min.
func atoiMin(raw string, fallback, min int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min {
		return min
	}
	return n
}

func portalPathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "id inválido")
	}
	return id, nil
}

// portalQueryID lê um id inteiro positivo de um parâmetro de query string —
// mesmo contrato de erro que portalPathID (path param), pra rotas tipo
// GET /portal/me/sessions?classId=123 que não têm o id no path.
func portalQueryID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, appErr(http.StatusBadRequest, "VALIDATION_ERROR", name+" inválido")
	}
	return id, nil
}

func decodePortalJSON(body io.Reader, v any) error {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// portalBodyJSON limits r.Body to 64 KiB and decodes it as strict JSON.
// All portal write handlers must call this instead of decodePortalJSON(r.Body, …)
// to prevent memory exhaustion from oversized payloads. The limit mirrors the
// 64 KiB cap used by every auth handler via http.MaxBytesReader.
func portalBodyJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	return decodePortalJSON(r.Body, v)
}

// ── Envelope de página ───────────────────────────────────────────────────────

type portalPage[T any] struct {
	Items      []T            `json:"items"`
	Total      int64          `json:"total"`
	Pagination portalPageMeta `json:"pagination"`
}

type portalPageMeta struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"totalPages"`
}

func newPortalPage[T any](items []T, total int64, p portalPagination) portalPage[T] {
	totalPages := int(math.Ceil(float64(total) / float64(p.Limit)))
	if totalPages < 1 {
		totalPages = 1
	}
	return portalPage[T]{Items: items, Total: total, Pagination: portalPageMeta{Page: p.Page, Limit: p.Limit, Total: total, TotalPages: totalPages}}
}

// ── DTOs de catálogo ─────────────────────────────────────────────────────────

type portalCourseDTO struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Description   *string `json:"description"`
	IsPaid        bool    `json:"isPaid"`
	DurationHours *int    `json:"durationHours"`
	Level         *string `json:"level"`
	Focus         *string `json:"focus"`
	Price         *string `json:"price"`
}

type portalModuleDTO struct {
	ID          string  `json:"id"`
	CourseID    string  `json:"courseId"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	IndexOrder  int     `json:"indexOrder"`
}

type portalPhaseDTO struct {
	ID             string    `json:"id"`
	ModuleID       string    `json:"moduleId"`
	Name           string    `json:"name"`
	WeekNumber     int       `json:"weekNumber"`
	IndexOrder     int       `json:"indexOrder"`
	AdminAuthorize bool      `json:"adminAuthorize"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// ── Inputs de catálogo ───────────────────────────────────────────────────────

type portalCourseInput struct {
	Name          string  `json:"name"`
	Description   *string `json:"description"`
	IsPaid        *bool   `json:"isPaid"`
	DurationHours *int    `json:"durationHours"`
	Level         *string `json:"level"`
	Focus         *string `json:"focus"`
	Price         *string `json:"price"`
}

func (in *portalCourseInput) validateCreate() error {
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) < 2 {
		return validationErr("nome obrigatório")
	}
	if in.DurationHours != nil && *in.DurationHours < 1 {
		return validationErr("durationHours deve ser positivo")
	}
	if in.Price != nil && strings.TrimSpace(*in.Price) == "" {
		return validationErr("price inválido")
	}
	return nil
}

type portalModuleInput struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	IndexOrder  *int    `json:"indexOrder"`
}

func (in *portalModuleInput) validateCreate() error {
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) < 2 {
		return validationErr("nome obrigatório")
	}
	if in.IndexOrder != nil && *in.IndexOrder < 1 {
		return validationErr("indexOrder deve ser positivo")
	}
	return nil
}

type portalPhaseInput struct {
	Name           string `json:"name"`
	WeekNumber     *int   `json:"weekNumber"`
	IndexOrder     *int   `json:"indexOrder"`
	AdminAuthorize *bool  `json:"adminAuthorize"`
}

func (in *portalPhaseInput) validateCreate() error {
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) < 2 {
		return validationErr("nome obrigatório")
	}
	if in.WeekNumber != nil && *in.WeekNumber < 1 {
		return validationErr("weekNumber deve ser positivo")
	}
	if in.IndexOrder != nil && *in.IndexOrder < 1 {
		return validationErr("indexOrder deve ser positivo")
	}
	return nil
}

// ── Turmas e matrículas ──────────────────────────────────────────────────────

type portalClassDTO struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	CourseID        string    `json:"courseId"`
	CurrentModuleID string    `json:"currentModuleId"`
	StartDate       time.Time `json:"startDate"`
	EndDate         time.Time `json:"endDate"`
	// IndividualClass separa AULA PARTICULAR (1:1) de TURMA (grupo). É outra
	// pergunta que enrollment.individual: aqui é o formato da aula, lá é a
	// condição da matrícula do aluno. As duas convivem — e a turma precisa da
	// sua, porque uma turma pode existir antes de ter qualquer matrícula (é o
	// caso de tudo que veio do sync do Notion).
	IndividualClass bool      `json:"individualClass"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// portalStudentDTO é um aluno matriculado numa turma. Individual marca a
// matrícula (não a turma) como particular — turma de grupo pode ter alunos
// misturados, um marcado como particular e o resto não (ver enrollment.individual).
type portalStudentDTO struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Role       int16  `json:"role"`
	Individual bool   `json:"individual"`
	// ContractedLessons: mesmo campo de portalStudentOverviewDTO — aqui é o
	// valor ATUAL da matrícula, pro admin ver o que já está salvo antes de
	// editar (ver AlunosSection no dashboard/web).
	ContractedLessons *int `json:"contractedLessons"`
}

// portalTeacherDTO é um professor vinculado a uma turma (class_teacher) —
// diferente de portalStudentDTO porque o vínculo não é uma matrícula
// (não tem o conceito de "particular").
type portalTeacherDTO struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  int16  `json:"role"`
}

type portalTeacherInput struct {
	TeacherID int64 `json:"teacherId"`
}

// portalStudentOverviewDTO é uma linha do dashboard administrativo de alunos
// (ver dashboard/web Home.tsx) — um aluno matriculado, com progresso agregado
// no curso. "1 aula" = 1 phase; totalPhases/completedPhases somam TODOS os
// módulos do curso (não só o módulo atual da turma — a pergunta aqui é
// "quantas aulas o programa inteiro tem", não "quantas o módulo atual tem").
// teacherName vem null até alguém popular class_teacher pra essa turma (ver
// portal_class_scope.go) — não existe hoje nenhuma fonte pra derivar isso
// automaticamente. Individual vem da matrícula (enrollment.individual), não
// da turma — uma turma de grupo pode ter um aluno marcado como particular
// dentro dela.
type portalStudentOverviewDTO struct {
	StudentID       string  `json:"studentId"`
	StudentName     string  `json:"studentName"`
	StudentEmail    string  `json:"studentEmail"`
	ClassID         string  `json:"classId"`
	ClassName       string  `json:"className"`
	CourseID        string  `json:"courseId"`
	CourseName      string  `json:"courseName"`
	TotalPhases     int     `json:"totalPhases"`
	CompletedPhases int     `json:"completedPhases"`
	TeacherName     *string `json:"teacherName"`
	Individual      bool    `json:"individual"`
	// ContractedLessons: pacote de aulas contratado pelo aluno (nil = não
	// preenchido — turma de grupo normalmente fica assim). Quando preenchido,
	// o front (dashboard/web) usa como denominador do progresso em vez de
	// TotalPhases (currículo do curso inteiro).
	ContractedLessons *int `json:"contractedLessons"`
	// AulasDadas: aulas que JÁ aconteceram (class_session até hoje), diferente
	// de TotalPhases, que é o currículo previsto do curso. O aluno pergunta
	// "quantas aulas eu já tive", não "quantas fases o curso tem".
	AulasDadas  int        `json:"aulasDadas"`
	Faltas      int        `json:"faltas"`
	NextClassAt *time.Time `json:"nextClassAt"`
}

// portalStudentIndividualInput é o corpo do PATCH que atualiza uma matrícula:
// marca/desmarca como particular e/ou define o pacote de aulas contratadas.
// Individual é sempre obrigatório no payload (todo PATCH reenvia o valor atual,
// mesmo quando só ContractedLessons mudou) — o zero-value de bool ausente no
// JSON é `false`, que resetaria sem querer o toggle "particular" se fosse opcional.
type portalStudentIndividualInput struct {
	Individual bool `json:"individual"`
	// ContractedLessons: pacote de aulas contratado (relevante sobretudo pra
	// matrícula particular). nil = não veio no payload, mantém o valor salvo —
	// não dá pra "limpar" com null explícito nesse desenho simples (ver spec).
	ContractedLessons *int `json:"contractedLessons,omitempty"`
}

func (in portalStudentIndividualInput) validate() error {
	if in.ContractedLessons != nil && *in.ContractedLessons < 1 {
		return validationErr("contractedLessons deve ser maior que zero")
	}
	return nil
}

// portalScheduleDTO é um horário fixo semanal da turma (class_schedule) —
// ex.: "toda terça 19h30–21h". StartTime/EndTime em "HH:MM" (não time.Time:
// é hora do relógio, sem fuso/data, não faz sentido como Time completo).
type portalScheduleDTO struct {
	ID        string `json:"id"`
	ClassID   string `json:"classId"`
	DayOfWeek int16  `json:"dayOfWeek"` // 0=domingo .. 6=sábado (bate com time.Weekday do Go)
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
	AulaCount int    `json:"aulaCount"`
}

type portalScheduleInput struct {
	DayOfWeek int16  `json:"dayOfWeek"`
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
	// AulaCount: quantas "aulas" (unidade de 1h) esse horário representa,
	// herdado por toda sessão gerada automaticamente a partir dele. Default 1
	// (turma de grupo, 1 encontro = 1 aula) — só aula particular de encontro
	// mais longo (ex.: 2h) precisa setar 2. validate() garante >= 1.
	AulaCount int `json:"aulaCount"`
}

var portalTimeRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func (in *portalScheduleInput) validate() error {
	if in.DayOfWeek < 0 || in.DayOfWeek > 6 {
		return validationErr("dayOfWeek deve ficar entre 0 (domingo) e 6 (sábado)")
	}
	if !portalTimeRe.MatchString(in.StartTime) || !portalTimeRe.MatchString(in.EndTime) {
		return validationErr("startTime/endTime devem estar no formato HH:MM")
	}
	if in.StartTime >= in.EndTime {
		return validationErr("startTime deve ser antes de endTime")
	}
	if in.AulaCount <= 0 {
		in.AulaCount = 1
	}
	return nil
}

type portalClassInput struct {
	Name            string `json:"name"`
	CourseID        int64  `json:"courseId"`
	CurrentModuleID int64  `json:"currentModuleId"`
	StartDate       string `json:"startDate"`
	DurationWeeks   int    `json:"durationWeeks"`
	IndividualClass *bool  `json:"individualClass"`
}

func (in *portalClassInput) validateCreate() error {
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) < 2 {
		return validationErr("nome obrigatório")
	}
	if in.CourseID <= 0 || in.CurrentModuleID <= 0 {
		return validationErr("courseId e currentModuleId são obrigatórios")
	}
	if in.DurationWeeks == 0 {
		in.DurationWeeks = 12
	}
	if in.DurationWeeks < 1 || in.DurationWeeks > 104 {
		return validationErr("durationWeeks deve ficar entre 1 e 104")
	}
	return nil
}

type portalAddStudentsInput struct {
	StudentIDs []int64 `json:"studentIds"`
}

// portalMaxIDsPerRequest — teto de ids numa lista de entrada. Sem teto, o
// limite efetivo era o corpo de 64 KiB, que comporta ~6 mil ids — cada um
// virando uma linha (e, antes, um round-trip) no banco.
const portalMaxIDsPerRequest = 500

// portalValidateIDs recusa lista vazia, ids não positivos e lote acima do teto.
func portalValidateIDs(field string, ids []int64) error {
	if len(ids) == 0 {
		return validationErr(field + " obrigatório")
	}
	if len(ids) > portalMaxIDsPerRequest {
		return validationErr(fmt.Sprintf("no máximo %d ids por requisição (recebidos %d)", portalMaxIDsPerRequest, len(ids)))
	}
	for _, id := range ids {
		if id <= 0 {
			return validationErr(field + " contém id inválido")
		}
	}
	return nil
}

func (in *portalAddStudentsInput) validate() error {
	return portalValidateIDs("studentIds", in.StudentIDs)
}

// portalParseDate aceita "YYYY-MM-DD"; vazio → hoje (UTC, à meia-noite).
func portalParseDate(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		now := portalNowUTC()
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, validationErr("startDate inválido (use YYYY-MM-DD)")
	}
	return t, nil
}

type portalReorderInput struct {
	Direction string `json:"direction"`
}

func (in *portalReorderInput) validate() error {
	if in.Direction != "up" && in.Direction != "down" {
		return validationErr(`direction deve ser "up" ou "down"`)
	}
	return nil
}

// portalCronogramaPhase é a fase como aparece no cronograma (agrupada por semana).
type portalCronogramaPhase struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Module string `json:"module"`
}

// ── Salas (class_rooms) ──────────────────────────────────────────────────────

type portalRoomDTO struct {
	ID            string     `json:"id"`
	ClassID       string     `json:"classId"`
	Name          string     `json:"name"`
	CreatedAt     time.Time  `json:"createdAt"`
	IsAuthorized  bool       `json:"isAuthorized"`
	TargetLimited *time.Time `json:"targetLimited"`
	Status        string     `json:"status"`
}

type portalRoomInput struct {
	Name          string  `json:"name"`
	IsAuthorized  *bool   `json:"isAuthorized"`
	TargetLimited *string `json:"targetLimited"`
}

func (in *portalRoomInput) validateCreate() error {
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) < 2 {
		return validationErr("nome obrigatório")
	}
	return nil
}

type portalRoomStatusInput struct {
	IsAuthorized *bool `json:"isAuthorized"`
}

// ── Helpers de erro ──────────────────────────────────────────────────────────

func validationErr(msg string) error {
	return appErr(http.StatusBadRequest, "VALIDATION_ERROR", msg)
}

func notFoundErr(entity string) error {
	return appErr(http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("%s não encontrado", entity))
}

func conflictErr(msg string) error {
	return appErr(http.StatusConflict, "CONFLICT", msg)
}
