package main

import (
	"encoding/json"
	"io"
	"net/http"
)

// Teto do corpo de preferências: free-form, mas pequeno (config de UI, não payload).
const maxPreferencesBytes = 4 << 10 // 4 KiB

// handlePreferencesUpdate (PATCH /auth/me/preferences) faz merge parcial das
// preferências de UI do usuário no JSONB users.preferences. Aceita qualquer objeto
// JSON (cada app guarda suas chaves), com teto de tamanho; devolve o objeto final.
func (s *Server) handlePreferencesUpdate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPreferencesBytes+1))
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if len(body) > maxPreferencesBytes {
		writeErr(w, appErr(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "preferências grandes demais (máx. 4KB)"))
		return
	}
	// Precisa ser um objeto JSON — array/escalar/null não fazem merge coerente.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "preferências devem ser um objeto JSON"))
		return
	}
	merged, err := s.upsertPreferences(r.Context(), userIDFrom(r), body)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preferences": merged})
}
