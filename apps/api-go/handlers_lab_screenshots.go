package main

// Captura de tela dos PCs do laboratório — handlers HTTP. Pedir e ver é
// admin-only; o envio da imagem é público (o PC não faz login), autenticado
// pelo mesmo segredo de dispositivo do heartbeat.

import (
	"net/http"
)

// ── admin ────────────────────────────────────────────────────────────────────

// POST /hour-lab-devices/{id}/screenshot — pede uma captura.
//
// O comando sai no próximo heartbeat (até ~30s); a imagem chega depois disso.
// Quem pediu fica registrado junto da captura.
func (s *Server) handleRequestLabDeviceScreenshot(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	actor := userIDFrom(r)
	if err := s.requestLabDeviceScreenshot(r.Context(), id, actor); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GET /hour-lab-devices/{id}/screenshots — histórico, mais recente primeiro.
func (s *Server) handleListLabDeviceScreenshots(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	shots, err := s.listLabDeviceScreenshots(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"screenshots": shots})
}

// DELETE /hour-lab-devices/{id}/screenshots/{shotId} — apaga a captura.
//
// Apaga o registro E o arquivo no R2. Se o arquivo não sair, a resposta é erro:
// dizer "apagado" com a imagem ainda no bucket seria pior que não ter o botão.
func (s *Server) handleDeleteLabDeviceScreenshot(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	shotID, err := hourUUIDFrom(r, "shotId", errScreenshotNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.deleteLabDeviceScreenshot(r.Context(), id, shotID); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── público (segredo do dispositivo) ─────────────────────────────────────────

// POST /public/lab-devices/screenshot — {deviceId, deviceSecret, jpeg, width, height}
//
// Só é aceito quando existe pedido recente do admin (ver
// screenshotPendingWindow): a rota é pública, então sem isso um PC poderia
// mandar imagem de tela quando quisesse.
func (s *Server) handleLabDeviceScreenshot(w http.ResponseWriter, r *http.Request) {
	const limite = maxScreenshotBytes + (1 << 20)
	r.Body = http.MaxBytesReader(w, r.Body, limite)
	var in struct {
		DeviceID     string `json:"deviceId"`
		DeviceSecret string `json:"deviceSecret"`
		JPEG         string `json:"jpeg"` // base64 puro
		Width        int    `json:"width"`
		Height       int    `json:"height"`
	}
	// decodeJSONLimit, não decodeJSON: o decodeJSON embute um teto de 1 MB que
	// PREVALECE sobre o MaxBytesReader de fora (o menor sempre ganha), e uma
	// tela 1440p vira ~1,3 MB em base64. Com o teto de 1 MB o upload morria e
	// o admin via "deviceId inválido" — erro que aponta pro campo errado.
	if err := decodeJSONLimit(r, &in, limite); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido ou grande demais"))
		return
	}
	if !uuidRe.MatchString(in.DeviceID) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "deviceId inválido"))
		return
	}
	if len(in.DeviceSecret) > 2*labDeviceSecretBytes {
		writeErr(w, errLabDeviceUnauthorized)
		return
	}
	jpeg, err := decodeScreenshot(in.JPEG)
	if err != nil {
		writeErr(w, err)
		return
	}
	shot, err := s.storeLabDeviceScreenshot(r.Context(), in.DeviceID, in.DeviceSecret, jpeg, in.Width, in.Height)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": shot.ID})
}
