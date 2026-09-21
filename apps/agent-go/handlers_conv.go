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
	Kind          string `json:"kind"`
	Model         string `json:"model"`
	ToolsDisabled bool   `json:"toolsDisabled"`
	WebSearch     bool   `json:"webSearch"`
	Effort        string `json:"effort"`
}

// normalizeKind valida o tipo da conversa. Vazio = chat (compatibilidade com os
// clientes antigos, que não mandam o campo).
func normalizeKind(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "", chatKind:
		return chatKind, nil
	case designKind:
		return designKind, nil
	default:
		return "", appErr(http.StatusBadRequest, "VALIDATION_ERROR", "kind inválido (use chat ou design)")
	}
}

// validEfforts são os níveis aceitos pelo claude CLI (--effort). Vazio = default.
var validEfforts = map[string]bool{"": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// validModels são os apelidos aceitos pelo claude CLI (--model). Mesma lista já
// usada em normalizeGenerate (handlers_generate_stream.go) para /claude/generate —
// aqui replicada porque handleCreateConversation/handleSetModel não passavam por
// nenhuma validação: o valor ia direto pro argv de --model sem allowlist (achado
// da auditoria de 2026-09), diferente de effort, que já tinha uma.
var validModels = map[string]bool{"": true, "sonnet": true, "opus": true, "haiku": true}

func (s *Server) handleListConversations(w http.ResponseWriter, r *http.Request) {
	kind, err := normalizeKind(r.URL.Query().Get("kind"))
	if err != nil {
		writeErr(w, err)
		return
	}
	convs, err := s.listConversationsByKind(r.Context(), userIDFrom(r), kind)
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
	if !validModels[model] {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "model inválido (use sonnet, opus ou haiku)"))
		return
	}
	if model == "" {
		model = s.cfg.DefaultModel
	}
	effort := strings.TrimSpace(body.Effort)
	if !validEfforts[effort] {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "effort inválido (low|medium|high|xhigh|max)"))
		return
	}
	kind, err := normalizeKind(body.Kind)
	if err != nil {
		writeErr(w, err)
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
		Kind:          kind,
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

	if conv.Kind == designKind {
		if err := s.bootstrapDesignWorkspace(conv); err != nil {
			writeErr(w, err)
			return
		}
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
