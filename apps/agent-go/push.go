package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const expoPushURL = "https://exp.host/--/api/v2/push/send"

var pushHTTP = &http.Client{Timeout: 15 * time.Second}

// POST /claude/push/register {token} — registra o Expo push token do dispositivo.
func (s *Server) handlePushRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.Token) == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "token obrigatório"))
		return
	}
	if err := s.savePushToken(r.Context(), userIDFrom(r), strings.TrimSpace(body.Token)); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type expoMessage struct {
	To    string         `json:"to"`
	Title string         `json:"title"`
	Body  string         `json:"body"`
	Sound string         `json:"sound"`
	Data  map[string]any `json:"data,omitempty"`
}

// notifyTurnDone envia push para os dispositivos do usuário quando o turno termina
// e ninguém está conectado (app em background). Requer Expo push tokens registrados.
func (s *Server) notifyTurnDone(ctx context.Context, conv *Conversation, status string) {
	tokens, err := s.pushTokensForUser(ctx, conv.UserID)
	if err != nil || len(tokens) == 0 {
		return
	}
	title := "ClaudeAgent"
	if conv.Title != nil && *conv.Title != "" {
		title = *conv.Title
	}
	body := "Turno concluído ✓"
	if status == StatusError {
		body = "Turno falhou ⚠️"
	}
	msgs := make([]expoMessage, 0, len(tokens))
	for _, t := range tokens {
		msgs = append(msgs, expoMessage{To: t, Title: title, Body: body, Sound: "default",
			Data: map[string]any{"conversationId": conv.ID}})
	}
	// (#7) Fire-and-forget: o POST ao Expo (até 15s de timeout) não deve bloquear o
	// fim do turno. Roda em goroutine própria, com contexto independente do request.
	go sendExpoPush(msgs)
}

func sendExpoPush(msgs []expoMessage) {
	payload, _ := json.Marshal(msgs)
	req, err := http.NewRequest(http.MethodPost, expoPushURL, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := pushHTTP.Do(req)
	if err != nil {
		slog.Warn("falha ao enviar push", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("expo push retornou erro", "status", resp.StatusCode)
	}
}
