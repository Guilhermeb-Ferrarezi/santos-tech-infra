package main

import (
	"context"
	"log/slog"

	"github.com/santos-tech/auth/db"
)

// notifyUser registra a notificação no histórico (sino no header do
// dashboard) e dispara o Web Push. As duas coisas são independentes —
// falha ao gravar o histórico não deve impedir o push (e vice-versa,
// já que enqueuePush já degrada sozinho sem VAPID configurado).
func (s *Server) notifyUser(ctx context.Context, userID int32, title, body, url string) {
	if _, err := s.q.InsertNotification(ctx, db.InsertNotificationParams{
		UserID: userID, Title: title, Body: body, Url: url,
	}); err != nil {
		slog.Error("falha ao gravar notificação no histórico (segue)", "userID", userID, "err", err)
	}
	s.enqueuePush(ctx, userID, title, body, url)
}
