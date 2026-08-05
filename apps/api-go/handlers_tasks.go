package main

import (
	"net/http"
	"strings"
)

var errTaskNotFound = appErr(http.StatusNotFound, "TASK_NOT_FOUND", "Tarefa não encontrada")
var errTaskCategoryNotFound = appErr(http.StatusNotFound, "TASK_CATEGORY_NOT_FOUND", "Categoria não encontrada")

func taskIDFrom(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		return "", errTaskNotFound
	}
	return id, nil
}

func validateTaskInput(in *TaskInput) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Título obrigatório")
	}
	if !validTaskStatuses[in.Status] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Status inválido")
	}
	if !validTaskPriorities[in.Priority] {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Prioridade inválida")
	}
	return nil
}

// canSeeTask: admin sempre; staff comum só se é responsável ou criador da
// tarefa (mesma regra de listTasks, aplicada a get/update/delete individual).
func canSeeTask(t *Task, requesterID int64, isAdmin bool) bool {
	if isAdmin {
		return true
	}
	if t.ResponsavelID != nil && *t.ResponsavelID == requesterID {
		return true
	}
	if t.CreatedBy != nil && *t.CreatedBy == requesterID {
		return true
	}
	return false
}

func (s *Server) requesterIsAdmin(r *http.Request) bool {
	u, err := s.cachedUserByID(r.Context(), userIDFrom(r))
	return err == nil && u != nil && u.Role == RoleAdmin
}

// GET /tasks
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	uid := userIDFrom(r)
	isAdmin := s.requesterIsAdmin(r)
	tasks, err := s.listTasks(r.Context(), uid, isAdmin)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

// GET /tasks/{id}
func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id, err := taskIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	task, err := s.getTask(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if task == nil || !canSeeTask(task, userIDFrom(r), s.requesterIsAdmin(r)) {
		writeErr(w, errTaskNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

// POST /tasks
func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in TaskInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateTaskInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	task, err := s.insertTask(r.Context(), in, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"task": task})
}

// PUT /tasks/{id}
// Ordem deliberada: valida o corpo (barato, sem tocar banco) ANTES de buscar a
// tarefa existente (toca banco) — barato-primeiro, e também é o que permite
// testar a validação de corpo sem precisar de um Postgres real (ver Task 4).
func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	id, err := taskIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in TaskInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateTaskInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	current, err := s.getTask(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if current == nil || !canSeeTask(current, userIDFrom(r), s.requesterIsAdmin(r)) {
		writeErr(w, errTaskNotFound)
		return
	}
	task, err := s.updateTask(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

// DELETE /tasks/{id}
func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	id, err := taskIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	current, err := s.getTask(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if current == nil || !canSeeTask(current, userIDFrom(r), s.requesterIsAdmin(r)) {
		writeErr(w, errTaskNotFound)
		return
	}
	if err := s.deleteTask(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /tasks/{id}/notes
func (s *Server) handleListTaskNotes(w http.ResponseWriter, r *http.Request) {
	id, err := taskIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	task, err := s.getTask(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if task == nil || !canSeeTask(task, userIDFrom(r), s.requesterIsAdmin(r)) {
		writeErr(w, errTaskNotFound)
		return
	}
	notes, err := s.listTaskNotes(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": notes})
}

// POST /tasks/{id}/notes
// Mesma ordem deliberada de handleUpdateTask: corpo primeiro, banco depois.
func (s *Server) handleAddTaskNote(w http.ResponseWriter, r *http.Request) {
	id, err := taskIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.Content = strings.TrimSpace(in.Content)
	if in.Content == "" {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Conteúdo obrigatório"))
		return
	}
	task, err := s.getTask(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if task == nil || !canSeeTask(task, userIDFrom(r), s.requesterIsAdmin(r)) {
		writeErr(w, errTaskNotFound)
		return
	}
	note, err := s.insertTaskNote(r.Context(), id, userIDFrom(r), in.Content)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"note": note})
}

// ── Categorias — admin-only ──────────────────────────────────────────────

// GET /tasks/categories
func (s *Server) handleListTaskCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.listTaskCategories(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": cats})
}

// POST /tasks/categories
func (s *Server) handleCreateTaskCategory(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Nome obrigatório"))
		return
	}
	cat, err := s.insertTaskCategory(r.Context(), in.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"category": cat})
}

// PUT /tasks/categories/{id}
func (s *Server) handleUpdateTaskCategory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeErr(w, errTaskCategoryNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Nome obrigatório"))
		return
	}
	cat, err := s.updateTaskCategory(r.Context(), id, in.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	if cat == nil {
		writeErr(w, errTaskCategoryNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"category": cat})
}

// DELETE /tasks/categories/{id}
func (s *Server) handleDeleteTaskCategory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeErr(w, errTaskCategoryNotFound)
		return
	}
	if err := s.deleteTaskCategory(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
