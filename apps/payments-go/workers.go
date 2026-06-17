package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/hibiken/asynq"
)

// HandleNotifyPaid envia os emails de confirmação de pagamento: aviso interno para
// o admin (PAYMENT_NOTIFY_EMAIL, opcional) e recibo para quem pagou. Roda como task
// asynq, com retry/backoff — se um email falhar, o erro retornado faz o asynq
// reagendar a task. Idempotência: enviar o email mais de uma vez é aceitável
// (melhor reentregar do que perder a notificação).
func (s *Server) HandleNotifyPaid(ctx context.Context, t *asynq.Task) error {
	if s.email == nil || s.store == nil {
		return nil
	}
	var p NotifyPaidPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		// payload corrompido: não adianta retentar.
		return fmt.Errorf("payload inválido: %w: %w", err, asynq.SkipRetry)
	}
	c, err := s.store.GetChargeByPublicToken(ctx, p.PublicToken)
	if err != nil {
		slog.Warn("aviso de pagamento: charge não encontrada", "token", p.PublicToken, "err", err)
		return err // retenta: a charge pode ainda não estar visível por replicação/latência
	}

	// 1) aviso interno para o admin
	if s.cfg.NotifyEmail != "" {
		subject := fmt.Sprintf("💰 Pagamento confirmado — R$ %.2f", float64(c.AmountCents)/100)
		if err := s.email.send(ctx, s.cfg.NotifyEmail, subject, paymentPaidEmailHTML(c)); err != nil {
			slog.Error("aviso de pagamento (admin): falha ao enviar email", "err", err)
			return err // retenta a task inteira
		}
	}

	// 2) recibo para quem pagou
	name, email, err := s.store.PayerEmailByCharge(ctx, c.ID)
	if err != nil {
		slog.Warn("aviso de pagamento: pagador não encontrado", "charge", c.ID, "err", err)
		return nil // sem pagador associado: nada a enviar, não adianta retentar
	}
	if email != "" {
		if err := s.email.send(ctx, email, "Pagamento confirmado — Santos Tech", paymentReceiptEmailHTML(name, c.AmountCents)); err != nil {
			slog.Error("aviso de pagamento (pagador): falha ao enviar email", "err", err)
			return err // retenta
		}
	}
	return nil
}
