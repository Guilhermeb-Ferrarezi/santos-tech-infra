package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// Sem o mTLS de volta (skip), autenticamos pelo segredo na URL que só a Efí
	// conhece (nós o registramos). Fail-closed sempre: sem secret, recusa.
	if s.cfg.EFIWebhookSecret == "" {
		slog.Error("webhook recusado: EFI_WEBHOOK_SECRET ausente")
		writeError(w, http.StatusServiceUnavailable, "webhook_unverifiable", "Webhook não verificável")
		return
	}
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
	// Resolve o store: campo pixWH injetável nos testes; caso contrário usa s.store.
	wh := s.pixWH
	if wh == nil {
		if s.store == nil { // guarda defensiva (testes sem DB)
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		wh = s.store
	}
	retry := false
	for _, ev := range evs {
		claimed, err := wh.ClaimWebhookEvent(r.Context(), ev.ID, ev.Type, ev.Raw)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", "Falha ao registrar evento")
			return
		}
		if !claimed {
			continue // já concluído, ou entrega concorrente em voo
		}
		switch s.applyPixEvent(r.Context(), wh, ev) {
		case whDone:
			// Fase 2: só agora o evento é consumido de vez.
			if err := wh.MarkWebhookDone(r.Context(), ev.ID); err != nil {
				slog.Error("webhook pix: efeito aplicado mas falhou ao encerrar o evento", "id", ev.ID, "err", err)
			}
		case whRetry:
			// Deixa 'pending' de propósito: a reentrega da Efí reprocessa.
			retry = true
		case whSkip:
			// Sem efeito agora, mas pode virar efeito depois (status ainda não final).
			// Também fica 'pending', porém sem pedir reentrega imediata.
		}
	}
	if retry {
		// 5xx SEM consumir o evento — é o que faz a Efí reentregar de verdade.
		writeError(w, http.StatusInternalServerError, "webhook_retry", "Falha ao processar evento; reenvie")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// webhookOutcome diz o que fazer com o evento reivindicado.
type webhookOutcome int

const (
	whDone  webhookOutcome = iota // efeito aplicado (ou nada a fazer nunca): encerra o evento
	whSkip                        // sem efeito agora; deixa 'pending' e responde 200
	whRetry                       // falha transitória; deixa 'pending' e responde 5xx
)

// applyPixEvent aplica o efeito de um evento do webhook pix. NÃO encerra o evento —
// quem faz isso é o chamador, e só quando o retorno é whDone. Esse contrato é o que
// impede um pagamento de ser descartado por um erro no meio do caminho.
func (s *Server) applyPixEvent(ctx context.Context, wh pixWebhookStore, ev WebhookEvent) webhookOutcome {
	switch ev.Type {
	case "CHARGE_PAID":
		// Não confiamos no corpo do webhook — confirmamos o status real na Efí
		// antes de marcar paga (evita forja de pagamento via corpo manipulado).
		if s.provider == nil {
			slog.Warn("webhook pix: provider não configurado", "corr", ev.CorrelationID)
			return whRetry
		}
		cr, qerr := s.provider.GetCharge(ctx, ev.CorrelationID)
		if qerr != nil {
			slog.Warn("webhook pix: falha ao confirmar status na Efí", "corr", ev.CorrelationID, "err", qerr)
			return whRetry
		}
		if cr.Status != "paid" {
			slog.Warn("webhook pix: status não confirmado como pago", "corr", ev.CorrelationID, "status", cr.Status)
			return whSkip
		}
		if err := wh.MarkChargePaid(ctx, ev.CorrelationID); err != nil {
			slog.Warn("falha ao marcar paga", "corr", ev.CorrelationID, "err", err)
			return whRetry
		}
		if tok, e := wh.PublicTokenByCorrelation(ctx, ev.CorrelationID); e == nil {
			s.invalidateChargeStatus(ctx, tok)
			s.publishChargePaid(ctx, tok)
			s.enqueueNotifyPaid(ctx, tok)
		}
		return whDone

	case "PAYOUT_RESULT":
		// Resultado final de um Pix Envio (saque). PayoutStatus já vem mapeado:
		// "completed" (REALIZADO) | "failed" (NAO_REALIZADO) | "" (não-final).
		// Casa o saque por idEnvio/e2eId — NÃO confunde com o pix recebido (CHARGE_PAID).
		if ev.PayoutStatus == "" {
			slog.Info("webhook pix: envio em estado não-final, sem ação", "idEnvio", ev.IDEnvio)
			return whSkip
		}
		if err := wh.SetWithdrawalStatus(ctx, ev.IDEnvio, ev.E2EID, ev.PayoutStatus); err != nil {
			slog.Warn("webhook pix: falha ao atualizar status do saque", "idEnvio", ev.IDEnvio, "status", ev.PayoutStatus, "err", err)
			return whRetry
		}
		return whDone

	default:
		slog.Info("evento efi ignorado", "type", ev.Type)
		return whDone
	}
}

// handleRecWebhook recebe as mudanças de status dos contratos de PIX Automático
// (webhook de recorrências, separado do webhook pix de débito). Autentica pelo mesmo
// segredo na URL do webhook pix. O débito de cada ciclo NÃO chega aqui — ele cai no
// webhook pix existente (txid = cobr, resolvido por correlation_id em MarkChargePaid).
func (s *Server) handleRecWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.EFIWebhookSecret == "" {
		slog.Error("webhook rec recusado: EFI_WEBHOOK_SECRET ausente")
		writeError(w, http.StatusServiceUnavailable, "webhook_unverifiable", "Webhook não verificável")
		return
	}
	// Mesmo esquema do webhook pix: segredo em ?token=; a Efí pode anexar um sufixo
	// de rota ao final da URL registrada, então removemos antes de comparar.
	got := strings.TrimSuffix(r.URL.Query().Get("token"), "/rec")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.EFIWebhookSecret)) != 1 {
		slog.Warn("webhook rec rejeitado: segredo inválido")
		writeError(w, http.StatusUnauthorized, "webhook_rejected", "Webhook não autenticado")
		return
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
			continue
		}
		// Avisa a tela de checkout (SSE) da nova situação (active/rejected/canceled/expired).
		s.publishRecurrenceStatus(r.Context(), rec.PublicToken, ev.Status)
		// "Cobrar na assinatura": ao aprovar, dispara a 1ª cobrança automática.
		if ev.Status == "active" {
			s.maybeChargeFirstInstallment(r.Context(), rec)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleCobrWebhook recebe as notificações da API Cobranças (boleto/cartão). A Efí
// POSTa form-urlencoded "notification=<token>" a cada mudança de status; buscamos o detalhe em
// GET /v1/notification/{token} e marcamos paga a cobrança casada pelo charge_id do Efí
// (provider_charge_id). Mesmo esquema de segredo em ?token= dos demais webhooks.
func (s *Server) handleCobrWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.EFIWebhookSecret == "" {
		slog.Error("webhook cobr recusado: EFI_WEBHOOK_SECRET ausente")
		writeError(w, http.StatusServiceUnavailable, "webhook_unverifiable", "Webhook não verificável")
		return
	}
	got := strings.TrimSuffix(r.URL.Query().Get("token"), "/cobr")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.EFIWebhookSecret)) != 1 {
		slog.Warn("webhook cobr rejeitado: segredo inválido")
		writeError(w, http.StatusUnauthorized, "webhook_rejected", "Webhook não autenticado")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Corpo inválido")
		return
	}
	// POST de validação/registro (corpo vazio): responder 200.
	if strings.TrimSpace(string(body)) == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	// A API de Cobranças da Efí POSTa form-urlencoded: "notification=<token>"
	// (às vezes com "&token=..." adicional). Parseamos isso primeiro; alguns
	// ambientes/testes mandam JSON {"notification":"..."} — fallback para esse caso.
	notification := ""
	if vals, perr := url.ParseQuery(strings.TrimSpace(string(body))); perr == nil {
		notification = vals.Get("notification")
	}
	if notification == "" {
		var payload struct {
			Notification string `json:"notification"`
		}
		if json.Unmarshal(body, &payload) == nil {
			notification = payload.Notification
		}
	}
	if notification == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "Payload inválido")
		return
	}
	// Resolve o store: campo cobrWH injetável nos testes; caso contrário usa s.store.
	wh := s.cobrWH
	if wh == nil {
		if s.store == nil { // guarda defensiva (testes sem DB)
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		wh = s.store
	}
	if s.efiCobr == nil { // boleto/cartão desabilitado
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	// Idempotência em DUAS FASES: reivindica o token de notificação como ID de evento
	// ('pending') e só o encerra ('done') depois do efeito aplicado. Consumir o ID antes
	// de buscar a notificação na Efí transformava o 502 abaixo num pedido de retry que
	// era no-op garantido — o pagamento sumia.
	// pay_webhook_events.payload é JSONB: o corpo da Efí vem form-urlencoded (não-JSON),
	// então persistimos um objeto JSON com o token (não o corpo cru, que estoura o jsonb).
	seenPayload, _ := json.Marshal(map[string]string{"notification": notification})
	claimed, err := wh.ClaimWebhookEvent(r.Context(), notification, "cobr_notification", seenPayload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao registrar evento")
		return
	}
	if !claimed {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	// NÃO confiamos no corpo do webhook — buscamos o status real na Efí pelo token.
	evs, err := s.efiCobr.GetCobrancaNotification(r.Context(), notification)
	if err != nil {
		// Evento continua 'pending': a reentrega da Efí reprocessa de verdade.
		slog.Warn("webhook cobr: falha ao buscar notificação", "err", err)
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao consultar a notificação")
		return
	}
	for _, ev := range evs {
		switch ev.Status {
		case "paid", "settled", "approved":
			// Validamos o charge_id contra o banco (MarkChargePaidByProviderID retorna false
			// se o provider_charge_id não existir localmente — evita transições espúrias).
			marked, err := wh.MarkChargePaidByProviderID(r.Context(), ev.ChargeID)
			if err != nil {
				// Erro de banco no meio do caminho: NÃO encerra o evento e pede reentrega.
				slog.Warn("webhook cobr: falha ao marcar paga", "charge_id", ev.ChargeID, "err", err)
				writeError(w, http.StatusInternalServerError, "webhook_retry", "Falha ao processar evento; reenvie")
				return
			}
			if !marked {
				continue // já estava paga/cancelada, ou charge_id desconhecido
			}
			if tok, e := wh.PublicTokenByProviderID(r.Context(), ev.ChargeID); e == nil && tok != "" {
				s.invalidateChargeStatus(r.Context(), tok)
				s.publishChargePaid(r.Context(), tok)
				s.enqueueNotifyPaid(r.Context(), tok)
			}

		case "unpaid":
			// Cartão recusado: mantém pendente (não marca pago). Logamos para acompanhamento;
			// a UI já sabe que o status permanece waiting/unpaid via polling/SSE.
			slog.Info("webhook cobr: cobrança recusada (unpaid)", "charge_id", ev.ChargeID)

		case "contested", "refunded":
			// Disputa ou estorno: sem ação automática no DB por ora — log para acompanhamento.
			slog.Info("webhook cobr: status avançado recebido", "charge_id", ev.ChargeID, "status", ev.Status)

		default:
			slog.Info("webhook cobr: status ignorado", "charge_id", ev.ChargeID, "status", ev.Status)
		}
	}
	// Fase 2: todos os efeitos deste token foram aplicados.
	if err := wh.MarkWebhookDone(r.Context(), notification); err != nil {
		slog.Error("webhook cobr: efeito aplicado mas falhou ao encerrar o evento", "notification", notification, "err", err)
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
