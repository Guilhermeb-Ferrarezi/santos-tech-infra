package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

func (s *Server) handleDotfyWebhook(w http.ResponseWriter, r *http.Request) {
	// Fail-closed: em produção, recusa eventos não autenticáveis (sem secret HMAC).
	// Sem isso, qualquer um poderia forjar um CHARGE_PAID e quitar uma cobrança sem pagar.
	if s.cfg.Production && s.cfg.DotfyWebhookSecret == "" {
		slog.Error("webhook recusado: DOTFY_WEBHOOK_SECRET ausente em produção")
		writeError(w, http.StatusServiceUnavailable, "webhook_unverifiable", "Webhook não verificável: secret não configurado")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Corpo inválido")
		return
	}
	ev, err := s.provider.ParseWebhook(r.Header, body)
	if err != nil {
		// Assinatura ausente/inválida ou payload não confiável → rejeita como não autenticado.
		slog.Warn("webhook dotfy rejeitado", "err", err)
		writeError(w, http.StatusUnauthorized, "webhook_rejected", "Webhook não autenticado")
		return
	}
	if s.store == nil { // guarda defensiva (testes)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	fresh, err := s.store.MarkWebhookSeen(r.Context(), ev.ID, ev.Type, ev.Raw)
	if err != nil {
		// erro de banco: responde 500 para o Dotfy reenviar depois.
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao registrar evento")
		return
	}
	if !fresh {
		writeJSON(w, http.StatusOK, map[string]bool{"duplicate": true}) // já processado
		return
	}
	switch ev.Type {
	case "CHARGE_PAID":
		if err := s.store.MarkChargePaid(r.Context(), ev.CorrelationID); err != nil {
			slog.Warn("falha ao marcar paga", "corr", ev.CorrelationID, "err", err)
		} else if tok, e := s.store.PublicTokenByCorrelation(r.Context(), ev.CorrelationID); e == nil {
			s.publishChargePaid(r.Context(), tok)
			s.notifyPaymentPaid(tok)
		}
	case "CHARGE_EXPIRED":
		if err := s.store.MarkChargeExpired(r.Context(), ev.CorrelationID); err != nil {
			slog.Warn("falha ao marcar expirada", "corr", ev.CorrelationID, "err", err)
		}
	case "CHARGE_CREATED":
		// no-op: a charge já foi criada por nós.
	default:
		slog.Info("evento dotfy ignorado", "type", ev.Type)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// notifyPaymentPaid avisa, na confirmação do pagamento, o admin
// (PAYMENT_NOTIFY_EMAIL, opcional) e a própria pessoa que pagou (recibo).
// Assíncrono (não segura o ACK do webhook) e best-effort. Usa context próprio
// porque o request já terá retornado.
func (s *Server) notifyPaymentPaid(publicToken string) {
	if s.email == nil || s.store == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c, err := s.store.GetChargeByPublicToken(ctx, publicToken)
		if err != nil {
			slog.Warn("aviso de pagamento: charge não encontrada", "err", err)
			return
		}
		// 1) aviso interno para o admin
		if s.cfg.NotifyEmail != "" {
			subject := fmt.Sprintf("💰 Pagamento confirmado — R$ %.2f", float64(c.AmountCents)/100)
			if err := s.email.send(ctx, s.cfg.NotifyEmail, subject, paymentPaidEmailHTML(c)); err != nil {
				slog.Error("aviso de pagamento (admin): falha ao enviar email", "err", err)
			}
		}
		// 2) recibo para quem pagou
		name, email, err := s.store.PayerEmailByCharge(ctx, c.ID)
		if err != nil {
			slog.Warn("aviso de pagamento: pagador não encontrado", "charge", c.ID, "err", err)
			return
		}
		if email != "" {
			if err := s.email.send(ctx, email, "Pagamento confirmado — Santos Tech", paymentReceiptEmailHTML(name, c.AmountCents)); err != nil {
				slog.Error("aviso de pagamento (pagador): falha ao enviar email", "err", err)
			}
		}
	}()
}
