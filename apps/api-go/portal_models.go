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

// ── Helpers de erro ──────────────────────────────────────────────────────────

func validationErr(msg string) error {
	return appErr(http.StatusBadRequest, "VALIDATION_ERROR", msg)
}

func notFoundErr(entity string) error {
	return appErr(http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("%s não encontrado", entity))
}
