package main

import (
	"fmt"
	"net/http"
)

type payDTO struct {
	AmountCents int64  `json:"amountCents"`
	BRCode      string `json:"brCode"`
	QRCode      string `json:"qrCode"`
	Status      string `json:"status"`
	DueDate     string `json:"dueDate"`
}

func (s *Server) handleGetPay(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetChargeByPublicToken(r.Context(), r.PathValue("token"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Cobrança não encontrada")
		return
	}
	writeJSON(w, http.StatusOK, payDTO{
		AmountCents: c.AmountCents, BRCode: c.BRCode, QRCode: c.QRCode, Status: c.Status, DueDate: c.DueDate,
	})
}

// SSE: envia o status atual e, quando o webhook publicar "paid", empurra e encerra.
func (s *Server) handlePayEvents(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	status, err := s.chargeStatus(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Cobrança não encontrada")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "sse_unsupported", "Streaming indisponível")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// estado atual já resolve quem chega depois do pagamento
	fmt.Fprintf(w, "event: status\ndata: %s\n\n", status)
	flusher.Flush()
	if status != "pending" {
		return
	}

	pubsub := s.subscribeCharge(r.Context(), token)
	defer pubsub.Close()
	ch := pubsub.Channel()

	// Re-checa após assinar: fecha a janela de corrida em que o pagamento cai
	// ENTRE o GetCharge inicial e o Subscribe (senão o evento publicado se perderia
	// e o stream ficaria aberto pra sempre). Re-checa direto no banco (ignora cache)
	// para não confiar num valor "pending" potencialmente defasado nesta corrida.
	if c2, err := s.store.GetChargeByPublicToken(r.Context(), token); err == nil && c2.Status != "pending" {
		s.cacheChargeStatus(r.Context(), token, c2.Status)
		fmt.Fprintf(w, "event: paid\ndata: paid\n\n")
		flusher.Flush()
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			if msg != nil && msg.Payload == "paid" {
				fmt.Fprintf(w, "event: paid\ndata: paid\n\n")
				flusher.Flush()
				return
			}
		}
	}
}
