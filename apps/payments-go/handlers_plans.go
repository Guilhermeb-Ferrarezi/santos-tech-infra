package main

import (
	"net/http"
	"strings"
)

func (s *Server) handleCreatePlan(w http.ResponseWriter, r *http.Request) {
	var in Plan
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || in.AmountCents <= 0 || in.DueDay < 1 || in.DueDay > 28 {
		writeError(w, http.StatusBadRequest, "invalid_body", "name, amountCents>0 e dueDay (1-28) obrigatórios")
		return
	}
	if err := s.store.CreatePlan(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao salvar plano")
		return
	}
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListPlans(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}
