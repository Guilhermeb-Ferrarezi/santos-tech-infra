package main

import (
	"net/http"
	"strings"
)

// customRoleJSON é a representação pública de um cargo.
func customRoleJSON(cr *CustomRole) map[string]any {
	return map[string]any{
		"id":          cr.ID,
		"name":        cr.Name,
		"description": cr.Description,
		"permissions": cr.Permissions,
		"createdAt":   cr.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"updatedAt":   cr.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// isValidUUID valida formato UUID (8-4-4-4-12 hex). Delega pro mesmo uuidRe
// (handlers_boards.go) usado no resto do código — duas implementações
// independentes da mesma checagem podiam divergir silenciosamente.
func isValidUUID(s string) bool {
	return uuidRe.MatchString(s)
}

// GET /auth/admin/custom-roles
func (s *Server) handleListCustomRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.listCustomRoles(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(roles))
	for i := range roles {
		out = append(out, customRoleJSON(&roles[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out})
}

// POST /auth/admin/custom-roles
func (s *Server) handleCreateCustomRole(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		Name        string              `json:"name"`
		Description *string             `json:"description"`
		Permissions map[string][]string `json:"permissions"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "name é obrigatório"))
		return
	}
	if body.Permissions == nil {
		body.Permissions = map[string][]string{}
	}
	cr, err := s.createCustomRole(r.Context(), strings.TrimSpace(body.Name), body.Description, body.Permissions)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"role": customRoleJSON(cr)})
}

// GET /auth/admin/custom-roles/{id}
func (s *Server) handleGetCustomRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isValidUUID(id) {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "id inválido"))
		return
	}
	cr, err := s.getCustomRole(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if cr == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "cargo não encontrado"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"role": customRoleJSON(cr)})
}

// PATCH /auth/admin/custom-roles/{id}
func (s *Server) handleUpdateCustomRole(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	id := r.PathValue("id")
	if !isValidUUID(id) {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "id inválido"))
		return
	}
	var body struct {
		Name        string              `json:"name"`
		Description *string             `json:"description"`
		Permissions map[string][]string `json:"permissions"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "name é obrigatório"))
		return
	}
	if body.Permissions == nil {
		body.Permissions = map[string][]string{}
	}
	cr, err := s.updateCustomRole(r.Context(), id, strings.TrimSpace(body.Name), body.Description, body.Permissions)
	if err != nil {
		writeErr(w, err)
		return
	}
	if cr == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "cargo não encontrado"))
		return
	}
	s.invalidateCustomRoleCache(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"role": customRoleJSON(cr)})
}

// DELETE /auth/admin/custom-roles/{id}
func (s *Server) handleDeleteCustomRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isValidUUID(id) {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "id inválido"))
		return
	}
	deleted, err := s.deleteCustomRole(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !deleted {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "cargo não encontrado"))
		return
	}
	s.invalidateCustomRoleCache(r.Context(), id)
	w.WriteHeader(http.StatusNoContent)
}
