package main

import (
	"encoding/json"
	"net/http"
	"time"
)

type previewInput struct {
	ScheduleCron string `json:"scheduleCron"`
	Timezone     string `json:"timezone"`
	Count        int    `json:"count"`
}

// handlePreview devolve as próximas N execuções de um cron, em UTC (ISO 8601),
// usando o mesmo nextRun() do scheduler — fonte única de verdade da prévia.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	var in previewInput
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "JSON inválido")
		return
	}
	if in.Timezone == "" {
		in.Timezone = "America/Sao_Paulo"
	}
	if in.Count <= 0 {
		in.Count = 3
	}
	if in.Count > 10 {
		in.Count = 10
	}
	if err := validateCron(in.ScheduleCron, in.Timezone); err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	next := make([]string, 0, in.Count)
	after := time.Now().UTC()
	for i := 0; i < in.Count; i++ {
		t, err := nextRun(in.ScheduleCron, in.Timezone, after)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		next = append(next, t.Format(time.RFC3339))
		after = t
	}
	writeJSON(w, http.StatusOK, map[string]any{"next": next})
}
