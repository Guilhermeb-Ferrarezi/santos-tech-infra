package main

import (
	"net/http"
	"strconv"
)

func (s *Server) handleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	var in Subscription
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	if in.StudentID == 0 || in.PlanID == 0 || in.AmountCents <= 0 || in.DueDay < 1 || in.DueDay > 28 {
		writeError(w, http.StatusBadRequest, "invalid_body", "studentId, planId, amountCents>0 e dueDay (1-28) obrigatórios")
		return
	}
	if err := s.store.CreateSubscription(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao criar assinatura")
		return
	}
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) handleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListSubscriptions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handlePatchSubscription(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	if in.Status != "active" && in.Status != "paused" && in.Status != "canceled" {
		writeError(w, http.StatusBadRequest, "invalid_body", "status deve ser active|paused|canceled")
		return
	}
	if err := s.store.SetSubscriptionStatus(r.Context(), id, in.Status); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao atualizar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": in.Status})
}
