package main

import (
	"encoding/json"
	"net/http"
)

// handleInternalSend expõe SendText para outros serviços do ecossistema.
// Protegido por X-Api-Key (igual ao DashAPIKey) — chamado pelo dashboard API
// para notificações de deploy.
func (s *Server) handleInternalSend(w http.ResponseWriter, r *http.Request) {
	if key := r.Header.Get("X-Api-Key"); key == "" || key != s.cfg.DashAPIKey {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var body struct {
		Phone string `json:"phone"`
		Text  string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Phone == "" || body.Text == "" {
		http.Error(w, "bad_request: phone e text obrigatórios", http.StatusBadRequest)
		return
	}

	if s.sender == nil || s.sender.accessToken == "" {
		http.Error(w, "whatsapp_not_configured", http.StatusServiceUnavailable)
		return
	}

	if err := s.sender.SendText(r.Context(), body.Phone, body.Text); err != nil {
		http.Error(w, "send_failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}
