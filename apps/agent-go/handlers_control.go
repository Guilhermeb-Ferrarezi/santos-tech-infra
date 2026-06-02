package main

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// loadConv carrega a conversa do usuário ou escreve o erro apropriado.
func (s *Server) loadConv(w http.ResponseWriter, r *http.Request) *Conversation {
	conv, err := s.conversationByID(r.Context(), r.PathValue("id"), userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return nil
	}
	if conv == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Conversa não encontrada"))
		return nil
	}
	return conv
}

// POST /claude/conversations/{id}/model — troca o modelo (aplica no próximo turno).
func (s *Server) handleSetModel(w http.ResponseWriter, r *http.Request) {
	conv := s.loadConv(w, r)
	if conv == nil {
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	_ = decodeJSON(r, &body)
	model := strings.TrimSpace(body.Model)
	if model == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "Campo 'model' obrigatório"))
		return
	}
	if err := s.updateConversationModel(r.Context(), conv.ID, conv.UserID, model); err != nil {
		writeErr(w, err)
		return
	}
	conv.Model = model
	writeJSON(w, http.StatusOK, conv)
}

// POST /claude/conversations/{id}/clear — zera o contexto (rotaciona o session_id).
func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	conv := s.loadConv(w, r)
	if conv == nil {
		return
	}
	newSession := newUUID()
	if err := s.rotateSession(r.Context(), conv.ID, conv.UserID, newSession); err != nil {
		writeErr(w, err)
		return
	}
	_ = s.insertMessage(r.Context(), &Message{
		ConversationID: conv.ID, Role: "system", Kind: "text",
		Content: map[string]any{"note": "context_cleared"},
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessionId": newSession})
}

// POST /claude/conversations/{id}/compact — resume o contexto e recomeça a sessão
// semeada com o resumo (slash command /compact não existe em modo headless).
func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	conv := s.loadConv(w, r)
	if conv == nil {
		return
	}
	if !conv.SessionStarted {
		writeErr(w, appErr(http.StatusBadRequest, "NOTHING_TO_COMPACT", "Sessão ainda não tem contexto"))
		return
	}

	// Roda um turno de sumarização, coletando o texto do assistente.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	const prompt = "Resuma de forma concisa todo o contexto, decisões e estado atual desta conversa, " +
		"para servir de memória ao continuar numa nova sessão. Responda apenas com o resumo."
	raw, err := s.mgr.RunTurnCollect(conv, prompt)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "COMPACT_FAILED", "Falha ao resumir: "+err.Error()))
		return
	}
	summary := strings.TrimSpace(raw)

	// Recomeça a sessão e deixa o resumo como seed do próximo turno.
	newSession := newUUID()
	if err := s.rotateSession(ctx, conv.ID, conv.UserID, newSession); err != nil {
		writeErr(w, err)
		return
	}
	if summary != "" {
		s.rdb.Set(ctx, "claude:seed:"+conv.ID, "Resumo da conversa anterior:\n"+summary, 7*24*time.Hour)
	}
	_ = s.insertMessage(ctx, &Message{
		ConversationID: conv.ID, Role: "system", Kind: "text",
		Content: map[string]any{"note": "compacted", "summary": summary},
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessionId": newSession, "summary": summary})
}
