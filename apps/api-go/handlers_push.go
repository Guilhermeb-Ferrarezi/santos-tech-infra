package main

import (
	"net/http"
	"strings"

	"github.com/santos-tech/auth/db"
)

// pushSubscribeInput espelha o retorno de PushSubscription.toJSON() no browser.
type pushSubscribeInput struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	UserAgent string `json:"userAgent"`
}

// POST /auth/push/subscribe — registra (ou atualiza, se o endpoint já existir)
// a subscription de push do navegador atual para o usuário logado.
func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.cfg.VAPIDPublicKey == "" || s.cfg.VAPIDPrivateKey == "" {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NOT_CONFIGURED", "Notificações push não configuradas"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in pushSubscribeInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	if in.Endpoint == "" || in.Keys.P256dh == "" || in.Keys.Auth == "" {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "endpoint e keys são obrigatórios"))
		return
	}
	var userAgent *string
	if ua := strings.TrimSpace(in.UserAgent); ua != "" {
		userAgent = &ua
	}
	sub, err := s.q.UpsertPushSubscription(r.Context(), db.UpsertPushSubscriptionParams{
		UserID:    int32(userIDFrom(r)),
		Endpoint:  in.Endpoint,
		P256dh:    in.Keys.P256dh,
		Auth:      in.Keys.Auth,
		UserAgent: userAgent,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": sub.ID})
}

// DELETE /auth/push/subscribe — remove a subscription do usuário logado (ex.:
// ele desativou notificações no menu). Só apaga se pertencer ao próprio
// requisitante — nunca deixa apagar subscription de outro user_id.
func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<10)
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	if in.Endpoint == "" {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "endpoint é obrigatório"))
		return
	}
	if _, err := s.q.DeletePushSubscriptionByEndpoint(r.Context(), db.DeletePushSubscriptionByEndpointParams{
		Endpoint: in.Endpoint,
		UserID:   int32(userIDFrom(r)),
	}); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
