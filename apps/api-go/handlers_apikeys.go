package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleCreateAPIKey cria um Personal Access Token para o usuário logado. O segredo
// completo só é devolvido aqui (não dá pra recuperar depois — guardamos só o hash).
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"expiresInDays"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "nome é obrigatório"))
		return
	}
	if len(name) > 100 {
		name = name[:100]
	}

	var expires *time.Time
	if body.ExpiresInDays > 0 {
		t := time.Now().Add(time.Duration(body.ExpiresInDays) * 24 * time.Hour)
		expires = &t
	}

	token, prefix, hash := newAPIKey()
	id, created, err := s.insertAPIKey(r.Context(), userIDFrom(r), name, prefix, hash, expires)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":        id,
		"name":      name,
		"prefix":    prefix,
		"token":     token, // mostrado só nesta resposta
		"createdAt": created.UTC().Format(time.RFC3339),
	})
}

// handleListAPIKeys lista os tokens do usuário (sem o segredo — só prefixo e metadados).
func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.listAPIKeys(r.Context(), userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apiKeys": keys})
}

// handleDeleteAPIKey revoga um token do próprio usuário.
func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "id inválido"))
		return
	}
	ok, err := s.deleteAPIKey(r.Context(), userIDFrom(r), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "token não encontrado"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
