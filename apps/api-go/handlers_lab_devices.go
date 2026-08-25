package main

// PCs do laboratório — handlers HTTP. Heartbeat é público (o PC não faz login;
// a credencial é o segredo de dispositivo emitido pelo servidor no primeiro
// heartbeat), o resto (listar/renomear/despairar/mandar aviso/resetar segredo)
// é admin-only, mesmo domínio de handlers_hour_sessions.go.

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// sshPublicKeyRe aceita o formato "authorized_keys" de uma linha: tipo da
// chave + base64, com comentário opcional. Não valida o base64 em si (o
// sshd rejeita sozinho na hora do login) — só barra lixo óbvio antes de
// gravar no banco.
var sshPublicKeyRe = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)) [A-Za-z0-9+/]+=* ?.*$`)

// ── público (sem auth) ───────────────────────────────────────────────────────

// POST /public/lab-devices/heartbeat — {deviceId, deviceSecret?, token?, appVersion?}
//
// O PC não faz login: a credencial é o deviceSecret, gerado pelo servidor no
// primeiro heartbeat de um deviceId e devolvido UMA única vez (campo
// deviceSecret da resposta) — o app grava em disco e manda em todo heartbeat
// seguinte, senão leva 401. Sem isso, saber o deviceId (que o próprio PC exibe
// num QR na tela) bastava pra receber o pairToken em texto puro.
//
// token é o mesmo token de sessão de horas que o app já guarda pareado (se
// houver); resolve pra current_session_id só se ainda for um token válido.
func (s *Server) handleLabDeviceHeartbeat(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		DeviceID     string  `json:"deviceId"`
		DeviceSecret string  `json:"deviceSecret"`
		Token        *string `json:"token"`
		AppVersion   string  `json:"appVersion"`
		// PreviousDeviceId: identidade anterior do PC (0.1.10+, uma vez após a
		// troca para o id derivado do MachineGuid). O servidor costura o registro
		// antigo na nova identidade em vez de deixar o mesmo PC duplicado.
		PreviousDeviceID string `json:"previousDeviceId"`
		// Nomes dos aplicativos abertos agora (0.1.9+). Ausente = app antigo, e
		// aí a última lista conhecida é mantida em vez de apagada.
		OpenApps []string `json:"openApps"`
		// SSHPublicKey: chave pública gerada localmente pelo autounattend.xml da
		// imagem Windows, mandada só no primeiro heartbeat da máquina — string
		// vazia nos seguintes mantém a já registrada (ver upsert em
		// hour_lab_devices.go). A privada NUNCA sai da máquina.
		SSHPublicKey string `json:"sshPublicKey"`
	}
	if err := decodeJSON(r, &in); err != nil || !uuidRe.MatchString(in.DeviceID) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "deviceId inválido"))
		return
	}
	// Segredo é hex de labDeviceSecretBytes bytes; qualquer coisa fora disso é
	// descartada antes de tocar o banco (evita comparar strings arbitrárias).
	if len(in.DeviceSecret) > 2*labDeviceSecretBytes {
		writeErr(w, errLabDeviceUnauthorized)
		return
	}
	if len(in.AppVersion) > 50 {
		in.AppVersion = in.AppVersion[:50]
	}
	// Chave SSH pública: formato solto (só o prefixo do tipo de chave), mas com
	// teto de tamanho — RSA 4096 dá ~800 bytes, ed25519 ~100; 2KB cobre qualquer
	// caso real e barra quem tentasse inflar a coluna.
	if len(in.SSHPublicKey) > 2048 {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "sshPublicKey grande demais"))
		return
	}
	if in.SSHPublicKey != "" && !sshPublicKeyRe.MatchString(in.SSHPublicKey) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "sshPublicKey em formato inválido"))
		return
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
	res, err := s.upsertLabDeviceHeartbeat(r.Context(), in.DeviceID, in.DeviceSecret, clientIP(r),
		in.AppVersion, sessionID, encodeOpenApps(in.OpenApps), previousUUID(in.PreviousDeviceID), in.SSHPublicKey)
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
	if res.ScreenshotRequested {
		// O app tira a foto da tela, MOSTRA o aviso na tela do PC e manda em
		// POST /public/lab-devices/screenshot.
		resp["screenshotRequested"] = true
	}
	if res.DeviceSecret != nil {
		// Única vez que o segredo trafega — o app PRECISA persistir agora.
		resp["deviceSecret"] = *res.DeviceSecret
	}
	if res.PreviousDeviceResolved {
		// Libera o app a esquecer o id anterior. Enquanto não sai, ele reenvia
		// previousDeviceId — é o que faz a migração sobreviver a um heartbeat
		// que chega antes do servidor certo ou com o registro antigo travado.
		resp["previousDeviceResolved"] = true
	}
	if len(s.cfg.FleetAdminSSHPublicKeys) > 0 {
		// Chave(s) do ADMIN, não do PC — o app instala isso no próprio
		// authorized_keys pra permitir SSH de fora pra dentro. Vai em todo
		// heartbeat (não só o primeiro): se a chave rodar, o PC precisa
		// convergir sozinho no próximo ciclo, sem esperar reinstalação.
		resp["adminSSHPublicKeys"] = s.cfg.FleetAdminSSHPublicKeys
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── admin ────────────────────────────────────────────────────────────────────

// GET /hour-lab-devices?limit=&offset= — paginado (limit default 200, máx 500);
// `total` deixa o front avisar quando há mais PCs do que a página mostra.
func (s *Server) handleListLabDevices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := labDevicesDefaultLimit
	if n, err := strconv.Atoi(strings.TrimSpace(q.Get("limit"))); err == nil && n > 0 {
		limit = min(n, labDevicesMaxLimit)
	}
	offset := 0
	if n, err := strconv.Atoi(strings.TrimSpace(q.Get("offset"))); err == nil && n > 0 {
		offset = n
	}
	devices, total, err := s.listLabDevices(r.Context(), limit, offset)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"devices": devices,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
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

// POST /hour-lab-devices/{id}/reset-secret — esquece o segredo do PC: o próximo
// heartbeat vira uma adoção nova (e devolve um segredo novo). Escotilha pra
// quando o PC perde a config mas mantém o device_uuid, ou quando o admin
// desconfia que outra máquina adotou o dispositivo antes dele.
func (s *Server) handleResetLabDeviceSecret(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.resetLabDeviceSecret(r.Context(), id); err != nil {
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

// previousUUID valida a identidade anterior antes de chegar na query: valor
// fora do formato UUID é ignorado, não vira erro — o heartbeat é a função
// crítica do app e não pode falhar por causa de um campo de migração.
func previousUUID(s string) string {
	if !uuidRe.MatchString(s) {
		return ""
	}
	return s
}
