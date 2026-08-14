package main

import (
	"net/http"
	"strconv"

	"github.com/santos-tech/auth/db"
)

// notificationLimit: quantas notificações o sino carrega de uma vez —
// suficiente pro popover, sem paginação (não é uma tela de histórico completo).
const notificationLimit = 30

func notificationJSON(n *db.DashboardNotification) map[string]any {
	m := map[string]any{
		"id":        n.ID,
		"title":     n.Title,
		"body":      n.Body,
		"url":       n.Url,
		"createdAt": n.CreatedAt.Time,
	}
	if n.ReadAt.Valid {
		m["readAt"] = n.ReadAt.Time
	} else {
		m["readAt"] = nil
	}
	return m
}

// GET /auth/notifications — últimas notificações do usuário logado + contagem
// de não-lidas (pro badge do sino não precisar de uma segunda chamada).
func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	uid := int32(userIDFrom(r))
	rows, err := s.q.ListNotificationsByUser(r.Context(), db.ListNotificationsByUserParams{
		UserID: uid, Limit: notificationLimit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	unread, err := s.q.CountUnreadNotifications(r.Context(), uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, n := range rows {
		out = append(out, notificationJSON(&n))
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": out, "unreadCount": unread})
}

// POST /auth/notifications/{id}/read
func (s *Server) handleMarkNotificationRead(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "id inválido"))
		return
	}
	if _, err := s.q.MarkNotificationRead(r.Context(), db.MarkNotificationReadParams{
		ID: id, UserID: int32(userIDFrom(r)),
	}); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /auth/notifications/read-all
func (s *Server) handleMarkAllNotificationsRead(w http.ResponseWriter, r *http.Request) {
	if _, err := s.q.MarkAllNotificationsRead(r.Context(), int32(userIDFrom(r))); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
