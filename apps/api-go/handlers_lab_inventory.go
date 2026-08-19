package main

// Inventário de software dos PCs do laboratório — handlers HTTP. A coleta é
// pública (o PC não faz login; credencial é o segredo de dispositivo, igual ao
// heartbeat); consultar o inventário e cadastrar os programas esperados é
// admin-only, mesmo domínio de handlers_lab_devices.go.

import (
	"net/http"
	"strconv"
	"strings"
)

// ── público (segredo do dispositivo) ─────────────────────────────────────────

// POST /public/lab-devices/inventory — {deviceId, deviceSecret, programs[]}
//
// Chamado pelo app depois do heartbeat, quando a lista muda (ou uma vez por
// dia). Substitui o inventário inteiro: é uma foto do estado atual da máquina.
func (s *Server) handleLabDeviceInventory(w http.ResponseWriter, r *http.Request) {
	// Inventário é maior que o heartbeat: algumas centenas de entradas com
	// nome, versão e fabricante cabem folgado em 256 KB.
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var in struct {
		DeviceID     string       `json:"deviceId"`
		DeviceSecret string       `json:"deviceSecret"`
		Programs     []LabProgram `json:"programs"`
	}
	if err := decodeJSON(r, &in); err != nil || !uuidRe.MatchString(in.DeviceID) {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "deviceId inválido"))
		return
	}
	if len(in.DeviceSecret) > 2*labDeviceSecretBytes {
		writeErr(w, errLabDeviceUnauthorized)
		return
	}
	if len(in.Programs) > maxInventoryPrograms {
		writeErr(w, errTooManyPrograms)
		return
	}
	if err := s.replaceLabDeviceInventory(r.Context(), in.DeviceID, in.DeviceSecret, in.Programs); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(in.Programs)})
}

// ── admin ────────────────────────────────────────────────────────────────────

// GET /hour-lab-devices/{id}/programs — inventário do PC cruzado com os
// programas esperados.
func (s *Server) handleGetLabDevicePrograms(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	inv, err := s.labDeviceInventory(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

// GET /hour-lab-expected-programs
func (s *Server) handleListExpectedPrograms(w http.ResponseWriter, r *http.Request) {
	items, err := s.listExpectedPrograms(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"programs": items})
}

// expectedProgramInput valida label e padrão. O padrão vai para um ILIKE
// '%...%', então caracteres de curinga do LIKE precisam ser barrados: um '%'
// solto casaria com qualquer coisa e marcaria o programa como instalado em todo
// PC.
func expectedProgramInput(r *http.Request) (label, pattern string, err error) {
	r.Body = http.MaxBytesReader(nil, r.Body, 4<<10)
	var in struct {
		Label        string `json:"label"`
		MatchPattern string `json:"matchPattern"`
	}
	if e := decodeJSON(r, &in); e != nil {
		return "", "", appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido")
	}
	label = strings.TrimSpace(in.Label)
	pattern = strings.TrimSpace(in.MatchPattern)
	if pattern == "" {
		pattern = label // o caso comum: o rótulo já é o trecho procurado
	}
	if label == "" || len(label) > 80 {
		return "", "", appErr(http.StatusBadRequest, "BAD_REQUEST", "Nome obrigatório (até 80 caracteres)")
	}
	if len(pattern) > 80 {
		return "", "", appErr(http.StatusBadRequest, "BAD_REQUEST", "Padrão longo demais (até 80 caracteres)")
	}
	if strings.ContainsAny(pattern, "%_\\") {
		return "", "", appErr(http.StatusBadRequest, "BAD_REQUEST",
			"O padrão não pode conter %, _ ou \\ — escreva só um trecho do nome do programa")
	}
	return label, pattern, nil
}

// POST /hour-lab-expected-programs — {label, matchPattern?}
func (s *Server) handleCreateExpectedProgram(w http.ResponseWriter, r *http.Request) {
	label, pattern, err := expectedProgramInput(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	e, err := s.createExpectedProgram(r.Context(), label, pattern)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"program": e})
}

// PATCH /hour-lab-expected-programs/{id} — {label, matchPattern?}
func (s *Server) handleUpdateExpectedProgram(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, errExpectedProgramNotFound)
		return
	}
	label, pattern, err := expectedProgramInput(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	e, err := s.updateExpectedProgram(r.Context(), id, label, pattern)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"program": e})
}

// DELETE /hour-lab-expected-programs/{id}
func (s *Server) handleDeleteExpectedProgram(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, errExpectedProgramNotFound)
		return
	}
	if err := s.deleteExpectedProgram(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
