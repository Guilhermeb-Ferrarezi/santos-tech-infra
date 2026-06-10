package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
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

func portalPaginationFrom(r *http.Request) portalPagination {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	page := atoiMin(r.URL.Query().Get("page"), 1, 1)
	limit := atoiMin(r.URL.Query().Get("limit"), 20, 1)
	if limit > 100 {
		limit = 100
	}
	return portalPagination{Page: page, Limit: limit, Offset: (page - 1) * limit, Query: q}
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

func decodePortalJSON(body io.Reader, v any) error {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
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
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type portalStudentDTO struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  int16  `json:"role"`
}

type portalClassInput struct {
	Name            string `json:"name"`
	CourseID        int64  `json:"courseId"`
	CurrentModuleID int64  `json:"currentModuleId"`
	StartDate       string `json:"startDate"`
	DurationWeeks   int    `json:"durationWeeks"`
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

// ── Helpers de erro ──────────────────────────────────────────────────────────

func validationErr(msg string) error {
	return appErr(http.StatusBadRequest, "VALIDATION_ERROR", msg)
}

func notFoundErr(entity string) error {
	return appErr(http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("%s não encontrado", entity))
}
