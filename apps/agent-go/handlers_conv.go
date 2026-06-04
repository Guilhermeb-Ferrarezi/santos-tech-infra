package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type createConvBody struct {
	Title         string `json:"title"`
	Repo          string `json:"repo"`
	Model         string `json:"model"`
	ToolsDisabled bool   `json:"toolsDisabled"`
	WebSearch     bool   `json:"webSearch"`
	Effort        string `json:"effort"`
}

// validEfforts são os níveis aceitos pelo claude CLI (--effort). Vazio = default.
var validEfforts = map[string]bool{"": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

func (s *Server) handleListConversations(w http.ResponseWriter, r *http.Request) {
	convs, err := s.listConversations(r.Context(), userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": convs})
}

func (s *Server) handleCreateConversation(w http.ResponseWriter, r *http.Request) {
	var body createConvBody
	_ = decodeJSON(r, &body)

	model := strings.TrimSpace(body.Model)
	if model == "" {
		model = s.cfg.DefaultModel
	}
	effort := strings.TrimSpace(body.Effort)
	if !validEfforts[effort] {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "effort inválido (low|medium|high|xhigh|max)"))
		return
	}

	conv := &Conversation{
		ID:            newUUID(),
		UserID:        userIDFrom(r),
		Model:         model,
		Status:        StatusIdle,
		SessionID:     newUUID(),
		ToolsDisabled: body.ToolsDisabled,
		WebSearch:     body.WebSearch,
		Effort:        effort,
	}
	conv.Workdir = filepath.Join(s.cfg.WorkspaceRoot, conv.ID)
	if t := strings.TrimSpace(body.Title); t != "" {
		conv.Title = &t
	}
	if repo := strings.TrimSpace(body.Repo); repo != "" {
		conv.Repo = &repo
	}

	if err := os.MkdirAll(conv.Workdir, 0o755); err != nil {
		writeErr(w, err)
		return
	}

	// Prepara o workspace: .mcp.json do GitHub e clone do repo (se informado).
	if err := s.prepareWorkspace(r.Context(), conv); err != nil {
		writeErr(w, err)
		return
	}

	if err := s.insertConversation(r.Context(), conv); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, conv)
}

func (s *Server) handleGetConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	conv, err := s.conversationByID(r.Context(), id, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if conv == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Conversa não encontrada"))
		return
	}
	msgs, err := s.listMessages(r.Context(), conv.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": conv, "messages": msgs})
}

func (s *Server) handleDeleteConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	conv, err := s.conversationByID(r.Context(), id, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if conv == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Conversa não encontrada"))
		return
	}
	if _, err := s.deleteConversation(r.Context(), id, userIDFrom(r)); err != nil {
		writeErr(w, err)
		return
	}
	// Limpa o workdir (best-effort).
	if conv.Workdir != "" && strings.HasPrefix(conv.Workdir, s.cfg.WorkspaceRoot) {
		_ = os.RemoveAll(conv.Workdir)
	}
	w.WriteHeader(http.StatusNoContent)
}
