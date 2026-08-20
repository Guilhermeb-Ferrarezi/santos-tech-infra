package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Endpoints compatíveis com a API OpenAI, para que o Santosfy (e ferramentas
// similares) use o agent-go como provider de AI com a URL base
// https://api.santos-tech.com/claude.
//
// Santosfy chama:
//   GET  /claude/models             → lista modelos disponíveis
//   POST /claude/chat/completions   → geração chat (sem stream por ora)
//
// Auth: mesmo esquema do /claude/generate — qualquer usuário autenticado
// (JWT de sessão, PAT st_... ou INTERNAL_SECRET via Bearer).

type oaiModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

var oaiModels = []oaiModel{
	{ID: "claude-sonnet-4-6", Object: "model", Created: 1749600000, OwnedBy: "anthropic"},
	{ID: "claude-opus-4-8", Object: "model", Created: 1749600000, OwnedBy: "anthropic"},
	{ID: "claude-haiku-4-5-20251001", Object: "model", Created: 1749600000, OwnedBy: "anthropic"},
	{ID: "sonnet", Object: "model", Created: 1749600000, OwnedBy: "anthropic"},
	{ID: "opus", Object: "model", Created: 1749600000, OwnedBy: "anthropic"},
	{ID: "haiku", Object: "model", Created: 1749600000, OwnedBy: "anthropic"},
}

// GET /claude/models — lista os modelos suportados no formato OpenAI.
func (s *Server) handleOAIModels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   oaiModels,
	})
}

type oaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type oaiChatRequest struct {
	Model    string       `json:"model"`
	Messages []oaiMessage `json:"messages"`
	Stream   bool         `json:"stream"` // ignorado — sempre síncrono
}

// POST /claude/chat/completions — geração no formato OpenAI chat.
// Converte as mensagens em prompt, roda o Claude via generateOnceWithTrace e
// devolve a resposta no envelope OpenAI.
func (s *Server) handleOAIChatCompletions(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var req oaiChatRequest
	if err := decodeJSONLimit(r, &req, maxGenerateBody); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if len(req.Messages) == 0 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "messages é obrigatório"))
		return
	}

	// Monta o prompt a partir das mensagens: System vira instrução de topo;
	// User/Assistant são formatados como diálogo.
	var systemParts, dialogParts []string
	for _, m := range req.Messages {
		switch strings.ToLower(m.Role) {
		case "system":
			systemParts = append(systemParts, strings.TrimSpace(m.Content))
		default:
			role := strings.ToUpper(m.Role[:1]) + m.Role[1:]
			dialogParts = append(dialogParts, fmt.Sprintf("%s: %s", role, strings.TrimSpace(m.Content)))
		}
	}
	var prompt strings.Builder
	if len(systemParts) > 0 {
		prompt.WriteString(strings.Join(systemParts, "\n"))
		prompt.WriteString("\n\n")
	}
	prompt.WriteString(strings.Join(dialogParts, "\n\n"))

	text, _, err := s.generateOnceWithTrace(r.Context(), "openai_compat", prompt.String(), req.Model, false)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "GENERATION_FAILED", err.Error()))
		return
	}

	var b [8]byte
	_, _ = rand.Read(b[:])
	id := "chatcmpl-" + hex.EncodeToString(b[:])

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]string{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		},
	})
}
