package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// verifyHMACSignature confere a assinatura HMAC-SHA256 do header
// "sha256=<hex>" contra o secret. Mesmo padrão fail-closed de
// verifyMetaSignature (handlers_instagram.go): secret vazio SEMPRE reprova —
// o caller já responde 503 antes de chegar aqui nesse caso.
func verifyHMACSignature(body []byte, signatureHeader, secret string) bool {
	if secret == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return false
	}
	sigBytes, err := hex.DecodeString(strings.TrimPrefix(signatureHeader, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), sigBytes)
}

// emailWebhookPayload é o corpo que o mailserver (Sieve pipe, ver
// notify-webhook.sh no repo email/) envia a cada entrega LMTP.
type emailWebhookPayload struct {
	To string `json:"to"`
}

// POST /webhooks/email/new — chamado pelo docker-mailserver (Sieve global
// "before" + extprograms pipe) quando um email é entregue em qualquer caixa.
// Responde 200 rápido e sempre, mesmo sem usuário correspondente ao endereço
// (alias, postmaster@, caixa compartilhada) — não é erro, só não há pra quem
// notificar. Só rejeita assinatura inválida/corpo malformado (mesmo
// raciocínio anti-retry-agressivo do webhook do Instagram).
func (s *Server) handleEmailWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.EmailWebhookSecret == "" {
		writeErr(w, appErr(http.StatusServiceUnavailable, "NOT_CONFIGURED", "Webhook de email não configurado"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if !verifyHMACSignature(body, r.Header.Get("X-ST-Signature"), s.cfg.EmailWebhookSecret) {
		writeErr(w, appErr(http.StatusUnauthorized, "INVALID_SIGNATURE", "Assinatura inválida"))
		return
	}
	var payload emailWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil || strings.TrimSpace(payload.To) == "" {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "JSON inválido"))
		return
	}
	user, err := s.userByEmail(r.Context(), strings.TrimSpace(payload.To))
	if err != nil {
		writeErr(w, err)
		return
	}
	if user != nil {
		s.enqueuePush(r.Context(), int32(user.ID), "Novo email", "Chegou um email novo na sua caixa", "/dashboard/email")
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
