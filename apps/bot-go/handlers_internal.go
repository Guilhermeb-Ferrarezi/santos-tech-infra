package main

import (
	"encoding/json"
	"net/http"
)

// handleInternalSend expõe envio de WhatsApp para outros serviços do ecossistema.
// Protegido por X-Api-Key (DASH_API_KEY). Chamado pelo dashboard API para
// notificações de deploy.
//
// Body: { phone, text, instance }
//   - instance: nome da instância Evolution a usar como remetente (obrigatório)
func (s *Server) handleInternalSend(w http.ResponseWriter, r *http.Request) {
	if key := r.Header.Get("X-Api-Key"); key == "" || key != s.cfg.DashAPIKey {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var body struct {
		Phone    string `json:"phone"`
		Text     string `json:"text"`
		Instance string `json:"instance"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Phone == "" || body.Text == "" || body.Instance == "" {
		http.Error(w, "bad_request: phone, text e instance obrigatórios", http.StatusBadRequest)
		return
	}

	if s.cfg.EvolutionAPIURL == "" || s.cfg.EvolutionAPIKey == "" {
		http.Error(w, "evolution_not_configured", http.StatusServiceUnavailable)
		return
	}

	evo := NewEvolutionClient(s.cfg.EvolutionAPIURL, s.cfg.EvolutionAPIKey, body.Instance)
	if err := evo.SendText(r.Context(), body.Phone, body.Text); err != nil {
		http.Error(w, "send_failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}
