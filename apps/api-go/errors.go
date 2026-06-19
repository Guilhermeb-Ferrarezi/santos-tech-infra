package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// AppError espelha o formato de erro da API TS: {code, message} + status.
type AppError struct {
	Status  int
	Code    string
	Message string
}

func (e *AppError) Error() string { return e.Message }

func appErr(status int, code, message string) *AppError {
	return &AppError{Status: status, Code: code, Message: message}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("falha ao serializar resposta JSON", "err", err)
	}
}

func writeErr(w http.ResponseWriter, err error) {
	rid := w.Header().Get("X-Request-Id")
	if ae, ok := err.(*AppError); ok {
		body := map[string]string{"code": ae.Code, "message": ae.Message}
		if rid != "" {
			body["request_id"] = rid
		}
		writeJSON(w, ae.Status, body)
		return
	}
	slog.Error("erro interno", "err", err)
	body := map[string]string{"code": "INTERNAL_ERROR", "message": "Erro interno do servidor"}
	if rid != "" {
		body["request_id"] = rid
	}
	writeJSON(w, http.StatusInternalServerError, body)
}
