package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// AppError espelha o formato de erro do ecossistema: {code, message} + status.
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
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	if ae, ok := err.(*AppError); ok {
		writeJSON(w, ae.Status, map[string]string{"code": ae.Code, "message": ae.Message})
		return
	}
	slog.Error("erro interno", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{
		"code": "INTERNAL_ERROR", "message": "Erro interno do servidor",
	})
}
