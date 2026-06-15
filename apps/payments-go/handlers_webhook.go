package main

import (
	"io"
	"log/slog"
	"net/http"
)

func (s *Server) handleDotfyWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Corpo inválido")
		return
	}
	ev, err := s.provider.ParseWebhook(r.Header, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_webhook", "Webhook inválido")
		return
	}
	if s.store == nil { // guarda defensiva (testes)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	fresh, err := s.store.MarkWebhookSeen(r.Context(), ev.ID, ev.Type, ev.Raw)
	if err != nil {
		// erro de banco: responde 500 para o Dotfy reenviar depois.
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao registrar evento")
		return
	}
	if !fresh {
		writeJSON(w, http.StatusOK, map[string]bool{"duplicate": true}) // já processado
		return
	}
	switch ev.Type {
	case "CHARGE_PAID":
		if err := s.store.MarkChargePaid(r.Context(), ev.CorrelationID); err != nil {
			slog.Warn("falha ao marcar paga", "corr", ev.CorrelationID, "err", err)
		}
	case "CHARGE_EXPIRED":
		if err := s.store.MarkChargeExpired(r.Context(), ev.CorrelationID); err != nil {
			slog.Warn("falha ao marcar expirada", "corr", ev.CorrelationID, "err", err)
		}
	case "CHARGE_CREATED":
		// no-op: a charge já foi criada por nós.
	default:
		slog.Info("evento dotfy ignorado", "type", ev.Type)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
