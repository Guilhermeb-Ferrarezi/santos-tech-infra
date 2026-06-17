package main

import (
	"context"
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
		slog.Warn("webhook dotfy rejeitado", "err", err, "id", r.Header.Get("X-Webhook-Id"), "body_len", len(body))
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
			// Invalida o cache de status (estado mudou pending→paid) e acorda o SSE.
			s.invalidateChargeStatus(r.Context(), tok)
			s.publishChargePaid(r.Context(), tok)
			// Notificação durável: enfileira em vez de disparar uma goroutine solta,
			// para que o retry/backoff do asynq cubra falha de envio ou restart.
			s.enqueueNotifyPaid(r.Context(), tok)
		}
	case "CHARGE_EXPIRED":
		if err := s.store.MarkChargeExpired(r.Context(), ev.CorrelationID); err != nil {
			slog.Warn("falha ao marcar expirada", "corr", ev.CorrelationID, "err", err)
		} else if tok, e := s.store.PublicTokenByCorrelation(r.Context(), ev.CorrelationID); e == nil {
			// Estado mudou pending→expired: invalida o cache.
			s.invalidateChargeStatus(r.Context(), tok)
		}
	case "CHARGE_CREATED":
		// no-op: a charge já foi criada por nós.
	default:
		slog.Info("evento dotfy ignorado", "type", ev.Type)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// enqueueNotifyPaid enfileira (asynq) a notificação de pagamento confirmado, que
// envia o aviso ao admin e o recibo ao pagador no worker, com retry/backoff. Não
// segura o ACK do webhook (o processamento é assíncrono). Fail-open com fallback:
//   - sem fila configurada (testes): cai para a goroutine inline best-effort;
//   - enqueue falhou (Redis instável): loga e cai para a goroutine, para não
//     perder a notificação só porque o enfileiramento momentâneo falhou.
func (s *Server) enqueueNotifyPaid(ctx context.Context, publicToken string) {
	if s.email == nil || s.store == nil || publicToken == "" {
		return
	}
	if s.queue != nil {
		if _, err := s.queue.EnqueueContext(ctx, newNotifyPaidTask(publicToken)); err == nil {
			return
		} else {
			slog.Error("falha ao enfileirar notificação de pagamento — fallback inline", "err", err, "token", publicToken)
		}
	}
	s.notifyPaidInline(publicToken)
}

// notifyPaidInline é o fallback best-effort (goroutine solta) quando a fila não
// está disponível. Usa context próprio porque o request já terá retornado.
func (s *Server) notifyPaidInline(publicToken string) {
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic em notifyPaidInline", "panic", rec, "token", publicToken)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := s.HandleNotifyPaid(ctx, newNotifyPaidTask(publicToken)); err != nil {
			slog.Error("notificação de pagamento (inline) falhou", "err", err, "token", publicToken)
		}
	}()
}
