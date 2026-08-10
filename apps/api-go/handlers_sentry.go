package main

import (
	"net/http"
	"strconv"
	"strings"
)

// handleSentryIssues lista as issues (erros) dos projetos do Sentry do
// ecossistema. Admin-only. Responde 503 se SENTRY_API_TOKEN não configurado.
func (s *Server) handleSentryIssues(w http.ResponseWriter, r *http.Request) {
	if s.sentry == nil || !s.sentry.enabled() {
		writeErr(w, appErr(http.StatusServiceUnavailable, "SENTRY_UNAVAILABLE", "Erros indisponível: SENTRY_API_TOKEN não configurado"))
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	issues, err := s.sentry.listIssues(r.Context(), sentryIssuesParams{
		Project:     strings.TrimSpace(q.Get("project")),
		Status:      strings.TrimSpace(q.Get("status")),
		Search:      q.Get("q"),
		StatsPeriod: strings.TrimSpace(q.Get("stats_period")),
		Limit:       limit,
	})
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "SENTRY_ERROR", "Erro ao consultar o Sentry"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": issues})
}

// handleSentryProjects devolve os slugs dos projetos do Sentry conhecidos,
// pra popular o filtro de projeto na tela de Erros.
func (s *Server) handleSentryProjects(w http.ResponseWriter, r *http.Request) {
	if s.sentry == nil || !s.sentry.enabled() {
		writeErr(w, appErr(http.StatusServiceUnavailable, "SENTRY_UNAVAILABLE", "Erros indisponível: SENTRY_API_TOKEN não configurado"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": sentryProjectSlugs()})
}

// handleSentryIssueDetail devolve o detalhe de uma issue — metadados +
// exceção/stacktrace do evento mais recente + tags — pra exibir sem sair do
// dashboard. Admin-only.
func (s *Server) handleSentryIssueDetail(w http.ResponseWriter, r *http.Request) {
	if s.sentry == nil || !s.sentry.enabled() {
		writeErr(w, appErr(http.StatusServiceUnavailable, "SENTRY_UNAVAILABLE", "Erros indisponível: SENTRY_API_TOKEN não configurado"))
		return
	}
	id := r.PathValue("id")
	detail, err := s.sentry.getIssueDetail(r.Context(), id)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "SENTRY_ERROR", "Erro ao consultar o Sentry"))
		return
	}
	writeJSON(w, http.StatusOK, detail)
}
