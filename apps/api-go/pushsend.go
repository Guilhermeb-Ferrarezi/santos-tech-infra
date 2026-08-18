package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/santos-tech/auth/db"
)

// pushPayload é o corpo serializado que o service worker recebe no evento
// "push" (ver web/public/sw.js do dashboard).
type pushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
}

// pushTTLSeconds: quanto tempo o serviço de push deve guardar a mensagem se o
// dispositivo estiver offline, antes de descartá-la — folgado o bastante pra
// cobrir um fim de semana sem abrir o navegador.
const pushTTLSeconds = 30 * 24 * 60 * 60

// sendPush envia uma notificação Web Push para uma única subscription. gone=true
// quando o serviço de push respondeu 404/410 (endpoint expirado/revogado pelo
// navegador) — sinal para o caller apagar a subscription do banco.
func (s *Server) sendPush(ctx context.Context, sub db.PushSubscription, payload pushPayload) (gone bool, err error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("marshal push payload: %w", err)
	}
	res, err := webpush.SendNotificationWithContext(ctx, body, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{Auth: sub.Auth, P256dh: sub.P256dh},
	}, &webpush.Options{
		Subscriber:      s.cfg.VAPIDSubject,
		VAPIDPublicKey:  s.cfg.VAPIDPublicKey,
		VAPIDPrivateKey: s.cfg.VAPIDPrivateKey,
		TTL:             pushTTLSeconds,
	})
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone {
		return true, nil
	}
	if res.StatusCode >= 300 {
		return false, fmt.Errorf("push service retornou status %d", res.StatusCode)
	}
	return false, nil
}
