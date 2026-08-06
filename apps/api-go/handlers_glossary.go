package main

import (
	"net/http"
	"strings"
)

var errGlossaryTermNotFound = appErr(http.StatusNotFound, "GLOSSARY_TERM_NOT_FOUND", "Termo não encontrado")

func validateGlossaryTermInput(in *GlossaryTermInput) error {
	in.Term = strings.TrimSpace(in.Term)
	in.Definicao = strings.TrimSpace(in.Definicao)
	if in.Term == "" {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Termo obrigatório")
	}
	if in.Definicao == "" {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Definição obrigatória")
	}
	return nil
}

// GET /glossary — qualquer sessão autenticada, sem checagem de permissão (não é
// dado sensível; quanto mais gente conseguir ver a definição, melhor).
func (s *Server) handleListGlossaryTerms(w http.ResponseWriter, r *http.Request) {
	terms, err := s.listGlossaryTerms(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"terms": terms})
}

// POST /glossary — admin-only
func (s *Server) handleCreateGlossaryTerm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in GlossaryTermInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateGlossaryTermInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	term, err := s.insertGlossaryTerm(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"term": term})
}

// PUT /glossary/{id} — admin-only
func (s *Server) handleUpdateGlossaryTerm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeErr(w, errGlossaryTermNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in GlossaryTermInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateGlossaryTermInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	term, err := s.updateGlossaryTerm(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	if term == nil {
		writeErr(w, errGlossaryTermNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"term": term})
}

// DELETE /glossary/{id} — admin-only
func (s *Server) handleDeleteGlossaryTerm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeErr(w, errGlossaryTermNotFound)
		return
	}
	if err := s.deleteGlossaryTerm(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
