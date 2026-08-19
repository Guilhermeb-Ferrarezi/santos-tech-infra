package main

// PCs do laboratório — handlers HTTP. Heartbeat é público (o PC não faz
// login; device_uuid gerado localmente é a única identidade), o resto
// (listar/renomear/despairar/mandar aviso) é admin-only, mesmo domínio de
// handlers_hour_sessions.go.

import (
	"net/http"
	"strings"
)

// ── público (sem auth) ───────────────────────────────────────────────────────

// POST /public/lab-devices/heartbeat — {deviceId, token?, appVersion?}
// token é o mesmo token de sessão de horas que o app já guarda pareado (se
// houver); resolve pra current_session_id só se ainda for um token válido.
func (s *Server) handleLabDeviceHeartbeat(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		DeviceID   string  `json:"deviceId"`
		Token      *string `json:"token"`
		AppVersion string  `json:"appVersion"`
	}
	if err := decodeJSON(r, &in); err != nil || !uuidRe.MatchString(in.DeviceID) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "deviceId inválido"))
		return
	}
	if len(in.AppVersion) > 50 {
		in.AppVersion = in.AppVersion[:50]
	}
	var sessionID *string
	if in.Token != nil && isValidHourSessionToken(*in.Token) {
		h, err := s.getHourSessionByTokenHash(r.Context(), sha256Hex(*in.Token))
		if err != nil {
			writeErr(w, err)
			return
		}
		if h != nil {
			sessionID = &h.ID
		}
	}
	res, err := s.upsertLabDeviceHeartbeat(r.Context(), in.DeviceID, clientIP(r), in.AppVersion, sessionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := map[string]any{
		"name":            res.Name,
		"unpairRequested": res.UnpairRequested,
	}
	if res.MessageID != nil {
		resp["message"] = map[string]any{"id": *res.MessageID, "text": *res.MessageText}
	}
	if res.PairToken != nil {
		resp["pairToken"] = *res.PairToken
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── admin ────────────────────────────────────────────────────────────────────

// GET /hour-lab-devices
func (s *Server) handleListLabDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.listLabDevices(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

// PATCH /hour-lab-devices/{id} — {name}
func (s *Server) handleRenameLabDevice(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Nome obrigatório (até 80 caracteres)"))
		return
	}
	d, err := s.renameLabDevice(r.Context(), id, in.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": d})
}

// POST /hour-lab-devices/pair — {deviceUuid, clientId} — pareamento via QR:
// PC mostra um QR com o próprio device_uuid, admin escaneia com o celular
// (já autenticado), confirma o cliente aqui, e o PC recebe o token sozinho
// no heartbeat seguinte (até ~30s), sem digitar nada.
func (s *Server) handlePairLabDevice(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		DeviceUUID string `json:"deviceUuid"`
		ClientID   string `json:"clientId"`
	}
	if err := decodeJSON(r, &in); err != nil || !uuidRe.MatchString(in.DeviceUUID) || !uuidRe.MatchString(in.ClientID) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "deviceUuid/clientId inválido"))
		return
	}
	client, err := s.getHourClient(r.Context(), in.ClientID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if client == nil {
		writeErr(w, errHourClientNotFound)
		return
	}
	h, err := s.pairLabDeviceViaQR(r.Context(), in.DeviceUUID, in.ClientID, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": h})
}

// POST /hour-lab-devices/{id}/unpair — o PC volta sozinho pra tela de colar
// link no próximo heartbeat (não afeta a sessão em si, só o pareamento local).
func (s *Server) handleUnpairLabDevice(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.requestLabDeviceUnpair(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /hour-lab-devices/{id} — remove o registro do PC (entrada fantasma
// de teste, ou máquina que saiu de operação). Sessões que ele já rodou não
// são afetadas.
func (s *Server) handleDeleteLabDevice(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.deleteLabDevice(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /hour-lab-devices/{id}/message — {text} — aviso mostrado uma vez na
// tela do PC no próximo heartbeat.
func (s *Server) handleSendLabDeviceMessage(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" || len(in.Text) > 300 {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Mensagem obrigatória (até 300 caracteres)"))
		return
	}
	if err := s.sendLabDeviceMessage(r.Context(), id, in.Text); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
