package main

// PCs do laboratório — handlers HTTP. Heartbeat é público (o PC não faz login;
// a credencial é o segredo de dispositivo emitido pelo servidor no primeiro
// heartbeat), o resto (listar/renomear/despairar/mandar aviso/resetar segredo)
// é admin-only, mesmo domínio de handlers_hour_sessions.go.

import (
	"math"
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
	// 32 KB, não 4 KB: sshPublicKey (2 KB) e diagnosticNote (2 KB) sozinhos já
	// estouravam o teto antigo, e openApps aceita 100 nomes de até 100 runas
	// (~10 KB). Com 4 KB, um PC com muitas janelas abertas levava 400 em TODO
	// heartbeat, sumia do dashboard e parava de receber comando — e a mensagem
	// de erro culpava o deviceId.
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
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
		// aí a última lista conhecida é mantida em vez de apagada. O primeiro é
		// o da janela em foco — o "em uso agora" que o dashboard destaca.
		OpenApps []string `json:"openApps"`
		// SSHPublicKey: chave pública gerada localmente pelo autounattend.xml da
		// imagem Windows, mandada só no primeiro heartbeat da máquina — string
		// vazia nos seguintes mantém a já registrada (ver upsert em
		// hour_lab_devices.go). A privada NUNCA sai da máquina.
		SSHPublicKey string `json:"sshPublicKey"`
		// DiagnosticNote: auto-diagnóstico opcional em texto livre (ex.: estado
		// do sshd/firewall/porta), mandado por scripts de instalação que não têm
		// outro jeito de reportar status pra quem não está com acesso físico/SSH
		// à máquina no momento. Mesma semântica "grava a última, vazio não
		// apaga" do SSHPublicKey.
		DiagnosticNote string `json:"diagnosticNote"`
		// Hostname: nome da máquina no Windows (o mesmo que o Tailscale usa como
		// nome do nó). Não vira o `name` do dispositivo — esse continua sendo o
		// apelido do admin; serve de rótulo enquanto ninguém renomeou o PC.
		Hostname string `json:"hostname"`
		// Uso da máquina agora, em porcento (0-100). Ausente = não reportou
		// neste heartbeat, e a última leitura conhecida é mantida. Valor fora da
		// faixa (ou NaN) é descartado em silêncio: a rota é pública, e uma
		// métrica ruim não pode derrubar o heartbeat, que é a função principal.
		CPUPercent *float64 `json:"cpuPercent"`
		RAMPercent *float64 `json:"ramPercent"`
		GPUPercent *float64 `json:"gpuPercent"`
		// Modelo da GPU, pra distinguir os rigs ("NVIDIA GeForce RTX 5060 Ti").
		GPUName string `json:"gpuName"`
	}
	// Erro de decode separado do deviceId inválido: juntar os dois fazia um
	// corpo grande demais ser reportado como "deviceId inválido", mandando quem
	// investiga procurar no lugar errado.
	if err := decodeJSONLimit(r, &in, 32<<10); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido ou grande demais"))
		return
	}
	if !uuidRe.MatchString(in.DeviceID) {
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
	if len(in.DiagnosticNote) > 2048 {
		in.DiagnosticNote = in.DiagnosticNote[:2048]
	}
	// Hostname e nome de GPU são texto que vai direto pra tela do admin: tira
	// controle/NUL (o Postgres recusa 0x00 em coluna text) e limita o tamanho.
	in.Hostname = sanitizeInventoryField(in.Hostname)
	in.GPUName = sanitizeInventoryField(in.GPUName)
	if len([]rune(in.Hostname)) > maxHostnameLength {
		in.Hostname = truncRunes(in.Hostname, maxHostnameLength)
	}
	if len([]rune(in.GPUName)) > maxGPUNameLength {
		in.GPUName = truncRunes(in.GPUName, maxGPUNameLength)
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
	res, err := s.upsertLabDeviceHeartbeat(r.Context(), labHeartbeat{
		DeviceUUID:     in.DeviceID,
		DeviceSecret:   in.DeviceSecret,
		IP:             clientIP(r),
		AppVersion:     in.AppVersion,
		SessionID:      sessionID,
		OpenApps:       encodeOpenApps(in.OpenApps),
		PreviousUUID:   previousUUID(in.PreviousDeviceID),
		SSHPublicKey:   in.SSHPublicKey,
		DiagnosticNote: in.DiagnosticNote,
		Hostname:       in.Hostname,
		GPUName:        in.GPUName,
		CPUPercent:     validPercent(in.CPUPercent),
		RAMPercent:     validPercent(in.RAMPercent),
		GPUPercent:     validPercent(in.GPUPercent),
	})
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
	if res.LockRequested {
		resp["lockRequested"] = true
	}
	if res.RestartRequested {
		resp["restartRequested"] = true
	}
	if res.ShutdownRequested {
		resp["shutdownRequested"] = true
	}
	if res.CommandID != nil {
		// Igual ao message: sempre volta (não só na entrega), e o app
		// deduplica localmente pelo id — o resultado (POST
		// /public/lab-devices/command-result) chega bem depois de rodar.
		resp["command"] = map[string]any{"id": *res.CommandID, "text": *res.CommandText}
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
	// Chave(s) do ADMIN, não do PC — o app instala isso no próprio
	// authorized_keys pra permitir SSH de fora pra dentro. SÓ para dispositivo
	// já revisado por um admin (res.Name != nil — nome é sempre atribuído
	// à mão, nunca pelo próprio PC, ver renameLabDevice): sem esta checagem,
	// QUALQUER heartbeat com um device_uuid novo (auto-registro, sem aprovação)
	// recebia a chave pública do admin da frota, permitindo a qualquer um na
	// internet obtê-la só batendo em /public/lab-devices/heartbeat. Vai em todo
	// heartbeat de um dispositivo já nomeado (não só o primeiro): se a chave
	// rodar, o PC precisa convergir sozinho no próximo ciclo, sem esperar
	// reinstalação.
	if len(s.cfg.FleetAdminSSHPublicKeys) > 0 && res.Name != nil {
		resp["adminSSHPublicKeys"] = s.cfg.FleetAdminSSHPublicKeys
	}
	writeJSON(w, http.StatusOK, resp)
}

// POST /public/lab-devices/command-result — {deviceId, deviceSecret, commandId, result}
//
// O PC manda quando termina de rodar o comando livre entregue no heartbeat.
// commandId tem que bater com o comando pendente ATUAL — um resultado
// atrasado de um comando já substituído por outro é descartado em silêncio
// (o PC não tem como saber que isso aconteceu, e não precisa saber).
func (s *Server) handleLabDeviceCommandResult(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var in struct {
		DeviceID     string `json:"deviceId"`
		DeviceSecret string `json:"deviceSecret"`
		CommandID    string `json:"commandId"`
		Result       string `json:"result"`
	}
	if err := decodeJSONLimit(r, &in, 16<<10); err != nil {
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
	if !uuidRe.MatchString(in.CommandID) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "commandId inválido"))
		return
	}
	if len(in.Result) > maxCommandResultLength {
		in.Result = in.Result[:maxCommandResultLength]
	}
	if err := s.storeLabDeviceCommandResult(r.Context(), in.DeviceID, in.DeviceSecret, in.CommandID, in.Result); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// POST /hour-lab-devices/{id}/lock — trava a tela no próximo heartbeat.
func (s *Server) handleLockLabDevice(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.requestLabDeviceLock(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /hour-lab-devices/{id}/restart
func (s *Server) handleRestartLabDevice(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.requestLabDeviceRestart(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /hour-lab-devices/{id}/shutdown
func (s *Server) handleShutdownLabDevice(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.requestLabDeviceShutdown(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /hour-lab-devices/{id}/command — {text} — comando PowerShell livre,
// rodado pelo watchdog (contexto SYSTEM) no próximo heartbeat. O resultado
// chega depois, em POST /public/lab-devices/command-result — ver GET
// /hour-lab-devices pra ler commandResult/commandResultAt.
func (s *Server) handleSendLabDeviceCommand(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" || len(in.Text) > maxCommandTextLength {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Comando obrigatório (até 4000 caracteres)"))
		return
	}
	if err := s.sendLabDeviceCommand(r.Context(), id, in.Text); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Tetos dos campos de texto que o heartbeat traz pra exibição.
const (
	maxHostnameLength = 100
	maxGPUNameLength  = 100
)

// validPercent aceita só porcentagem plausível (0-100 e finita). Fora disso
// devolve nil, que o SQL trata como "não veio" e mantém a leitura anterior —
// nunca grava 0% num PC de onde chegou lixo, porque 0% na tela é indistinguível
// de "máquina parada" e mandaria o admin investigar o problema errado.
func validPercent(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 || *v > 100 {
		return nil
	}
	return v
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
