package main

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// wsInbound é a mensagem que o cliente envia pelo WebSocket.
type wsInbound struct {
	Type        string       `json:"type"` // prompt|interrupt
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"` // imagens/PDFs (base64) do whats-agent
}

// handleConversationWS é o endpoint de chat em tempo real.
// Autentica antes do upgrade (cookie access_token ou Bearer), depois transmite os
// eventos do turno e aceita prompts/interrupções.
func (s *Server) handleConversationWS(w http.ResponseWriter, r *http.Request) {
	uid, err := s.authenticate(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	id := r.PathValue("id")
	conv, err := s.conversationByID(r.Context(), id, uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if conv == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Conversa não encontrada"))
		return
	}

	// Validação de Origin protege contra CSWSH (a auth por cookie é cross-site sem isso).
	// Em produção NUNCA pulamos a checagem: sem CORS_ORIGIN, o default exige mesma origem.
	opts := &websocket.AcceptOptions{OriginPatterns: s.cfg.CORSOrigins}
	if len(s.cfg.CORSOrigins) == 0 && !s.cfg.Production {
		opts.InsecureSkipVerify = true // só em dev
	}
	c, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer c.CloseNow()
	// Anexos de mídia chegam em base64 (até 4 × 5 MB ≈ 27 MB de JSON);
	// o default do coder/websocket é 32 KB.
	c.SetReadLimit(32 << 20)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Assina os eventos da conversa. O turno roda independente deste WS (background),
	// então sair do app NÃO mata o turno; reconectar reata o stream ao vivo.
	events, unsub := s.mgr.Subscribe(conv.ID)
	defer unsub()

	// Writer único: drena o canal de eventos para o WS.
	go func() {
		// recover() é o PRIMEIRO defer: um panic aqui (ex: write em conexão já
		// fechada) não pode derrubar o processo. cancel() encerra o reader.
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic no writer do WebSocket",
					"conv", conv.ID, "panic", rec, "stack", string(debug.Stack()))
			}
			cancel()
		}()
		for ev := range events {
			if err := wsjson.Write(ctx, c, ev); err != nil {
				return
			}
		}
	}()

	for {
		var msg wsInbound
		if err := wsjson.Read(ctx, c, &msg); err != nil {
			return // conexão fechada
		}
		switch msg.Type {
		case "prompt":
			fresh, err := s.conversationByID(ctx, id, uid)
			if err != nil || fresh == nil {
				s.mgr.dispatch(conv.ID, turnEvent{Type: "error", Code: "NOT_FOUND", Message: "Conversa não encontrada"})
				continue
			}
			go s.mgr.RunTurn(fresh, msg.Text, msg.Attachments)
		case "interrupt":
			s.mgr.Interrupt(id)
		}
	}
}
