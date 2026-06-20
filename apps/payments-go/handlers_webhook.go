package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// Sem o mTLS de volta (skip), autenticamos pelo segredo na URL que só a Efí
	// conhece (nós o registramos). Fail-closed em produção: sem secret, recusa.
	if s.cfg.Production && s.cfg.EFIWebhookSecret == "" {
		slog.Error("webhook recusado: EFI_WEBHOOK_SECRET ausente em produção")
		writeError(w, http.StatusServiceUnavailable, "webhook_unverifiable", "Webhook não verificável")
		return
	}
	if s.cfg.EFIWebhookSecret != "" {
		// O segredo vai como ?token= (o redactor mascara chaves contendo "token";
		// "hmac" vazaria no Loki). A Efí ANEXA "/pix" ao FINAL da URL registrada —
		// como o token é o último elemento, o "/pix" gruda no valor ("<secret>/pix").
		// Por isso removemos esse sufixo antes de comparar (a validação do registro
		// vem sem "/pix", as notificações vêm com).
		got := strings.TrimSuffix(r.URL.Query().Get("token"), "/pix")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.EFIWebhookSecret)) != 1 {
			slog.Warn("webhook efi rejeitado: segredo inválido")
			writeError(w, http.StatusUnauthorized, "webhook_rejected", "Webhook não autenticado")
			return
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Corpo inválido")
		return
	}
	evs, err := s.provider.ParseWebhook(r.Header, body)
	if err != nil {
		slog.Warn("webhook efi: payload inválido", "err", err, "body_len", len(body))
		writeError(w, http.StatusBadRequest, "invalid_body", "Payload inválido")
		return
	}
	if s.store == nil { // guarda defensiva (testes)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	for _, ev := range evs {
		fresh, err := s.store.MarkWebhookSeen(r.Context(), ev.ID, ev.Type, ev.Raw)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", "Falha ao registrar evento")
			return
		}
		if !fresh {
			continue // já processado
		}
		switch ev.Type {
		case "CHARGE_PAID":
			if err := s.store.MarkChargePaid(r.Context(), ev.CorrelationID); err != nil {
				slog.Warn("falha ao marcar paga", "corr", ev.CorrelationID, "err", err)
			} else if tok, e := s.store.PublicTokenByCorrelation(r.Context(), ev.CorrelationID); e == nil {
				s.invalidateChargeStatus(r.Context(), tok)
				s.publishChargePaid(r.Context(), tok)
				s.enqueueNotifyPaid(r.Context(), tok)
			}
		default:
			slog.Info("evento efi ignorado", "type", ev.Type)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleRecWebhook recebe as mudanças de status dos contratos de PIX Automático
// (webhook de recorrências, separado do webhook pix de débito). Autentica pelo mesmo
// segredo na URL do webhook pix. O débito de cada ciclo NÃO chega aqui — ele cai no
// webhook pix existente (txid = cobr, resolvido por correlation_id em MarkChargePaid).
func (s *Server) handleRecWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Production && s.cfg.EFIWebhookSecret == "" {
		slog.Error("webhook rec recusado: EFI_WEBHOOK_SECRET ausente em produção")
		writeError(w, http.StatusServiceUnavailable, "webhook_unverifiable", "Webhook não verificável")
		return
	}
	if s.cfg.EFIWebhookSecret != "" {
		// Mesmo esquema do webhook pix: segredo em ?token=; a Efí pode anexar um sufixo
		// de rota ao final da URL registrada, então removemos antes de comparar.
		got := strings.TrimSuffix(r.URL.Query().Get("token"), "/rec")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.EFIWebhookSecret)) != 1 {
			slog.Warn("webhook rec rejeitado: segredo inválido")
			writeError(w, http.StatusUnauthorized, "webhook_rejected", "Webhook não autenticado")
			return
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Corpo inválido")
		return
	}
	// POST de validação da Efí no registro do webhook (corpo vazio): responder 200.
	if strings.TrimSpace(string(body)) == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	evs, err := s.efi.ParseRecWebhook(r.Header, body)
	if err != nil {
		slog.Warn("webhook rec: payload inválido", "err", err, "body_len", len(body))
		writeError(w, http.StatusBadRequest, "invalid_body", "Payload inválido")
		return
	}
	if s.store == nil { // guarda defensiva (testes)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	for _, ev := range evs {
		if ev.EfiIDRec == "" {
			continue
		}
		rec, err := s.store.GetRecurrenceByEfiID(r.Context(), ev.EfiIDRec)
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("webhook rec: recorrência desconhecida", "idRec", ev.EfiIDRec)
			continue
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", "Falha ao carregar recorrência")
			return
		}
		if ev.Status == "" || ev.Status == rec.Status {
			continue // sem mudança a aplicar
		}
		if err := s.store.SetRecurrenceStatus(r.Context(), rec.ID, ev.Status); err != nil {
			slog.Warn("webhook rec: falha ao atualizar status", "rec", rec.ID, "status", ev.Status, "err", err)
		}
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
