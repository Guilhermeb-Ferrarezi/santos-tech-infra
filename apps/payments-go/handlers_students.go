package main

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleCreateStudent(w http.ResponseWriter, r *http.Request) {
	var in Student
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.TaxID = strings.TrimSpace(in.TaxID)
	in.Email = strings.TrimSpace(in.Email)
	if in.Name == "" || in.TaxID == "" || in.Email == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "name, taxId e email são obrigatórios")
		return
	}
	if len(in.TaxID) != 11 || strings.IndexFunc(in.TaxID, func(r rune) bool { return r < '0' || r > '9' }) != -1 {
		writeError(w, http.StatusBadRequest, "invalid_body", "taxId deve ter exatamente 11 dígitos numéricos")
		return
	}
	if !strings.Contains(in.Email, "@") || !strings.Contains(in.Email, ".") {
		writeError(w, http.StatusBadRequest, "invalid_body", "email inválido")
		return
	}
	if err := s.store.CreateStudent(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao salvar aluno")
		return
	}
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) handleListStudents(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListStudents(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetStudent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	st, err := s.store.GetStudent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Aluno não encontrado")
		return
	}
	writeJSON(w, http.StatusOK, st)
}
