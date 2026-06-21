package main

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// hasSensitiveCardFields verifica se o JSON bruto contém campos de PAN/CVV.
// Combina duas estratégias:
// 1. Varredura recursiva procurando chaves inequivocamente de cartão (cvv/pan/card_number/cvc).
// 2. Checagem de campos no nível raiz que não pertencem ao CardChargeInput (inclui "number").
// billingAddress.number (número da rua) é um campo legítimo e não é flagrado.
// Nunca loga o conteúdo do body.
func hasSensitiveCardFields(raw []byte) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	return containsSensitiveKey(v)
}

// panCVVFieldsDeep são chaves que indicam PAN/CVV independente do nível de aninhamento.
// Excluímos "number" aqui porque billingAddress.number é legítimo (número de rua).
// "number" no nível raiz ainda é bloqueado pela checagem de campos desconhecidos.
var panCVVFieldsDeep = []string{"cvv", "card_number", "card_cvv", "pan", "cvc", "cvc2"}

// knownTopLevelFields são os campos permitidos no nível raiz de CardChargeInput.
var knownTopLevelFields = map[string]bool{
	"paymentToken":   true,
	"installments":   true,
	"customer":       true,
	"billingAddress": true,
	"amountCents":    true,
	"description":    true,
	"customerId":     true,
}

// containsSensitiveKey percorre recursivamente interface{} procurando chaves de PAN/CVV.
// Para o nível raiz (depth=0) também rejeita campos desconhecidos que não pertencem ao
// CardChargeInput (incluindo "number" que poderia ser PAN).
func containsSensitiveKey(v any) bool {
	return checkSensitive(v, true)
}

func checkSensitive(v any, isRoot bool) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			kl := strings.ToLower(k)
			// Campos inequivocamente de cartão em qualquer nível.
			for _, f := range panCVVFieldsDeep {
				if kl == f {
					return true
				}
			}
			// No nível raiz, "number" e campos desconhecidos são rejeitados.
			if isRoot && !knownTopLevelFields[k] {
				return true
			}
			if checkSensitive(child, false) {
				return true
			}
		}
	case []any:
		for _, item := range t {
			if checkSensitive(item, false) {
				return true
			}
		}
	}
	return false
}

// handleCreateCardCharge (POST /charges/card) cria uma cobrança de cartão.
// Recebe SOMENTE payment_token + dados não-sensíveis (installments, customer,
// billing_address). Rejeita com 400 qualquer corpo que contenha PAN/CVV.
func (s *Server) handleCreateCardCharge(w http.ResponseWriter, r *http.Request) {
	if s.efiCobr == nil {
		writeError(w, http.StatusServiceUnavailable, "card_unavailable", "Pagamento com cartão indisponível no momento")
		return
	}

	// Lê o corpo bruto para inspecionar campos sensíveis ANTES de qualquer Unmarshal.
	// Limitar a 1 MB — payloads legítimos são pequenos.
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Corpo inválido")
		return
	}

	// Fail-closed: se contiver PAN/CVV, rejeita imediatamente e NÃO loga o corpo.
	if hasSensitiveCardFields(raw) {
		slog.Warn("handleCreateCardCharge: corpo rejeitado por conter campos sensíveis de cartão")
		writeError(w, http.StatusBadRequest, "sensitive_fields", "O corpo da requisição não deve conter número de cartão ou CVV. Use payment_token.")
		return
	}

	var in CardChargeInput
	if err := json.Unmarshal(raw, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}

	if in.PaymentToken == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "payment_token é obrigatório")
		return
	}
	if in.AmountCents <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "amountCents deve ser > 0")
		return
	}
	if in.Customer.Name == "" || in.Customer.TaxID == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "customer.name e customer.taxId são obrigatórios")
		return
	}

	// Cria no gateway Efí.
	result, err := s.efiCobr.ChargeCardOneStep(r.Context(), in)
	if err != nil {
		var pe *ProviderError
		if errors.As(err, &pe) {
			slog.Warn("card charge: erro do gateway", "status", pe.Status, "message", pe.Message)
			writeError(w, http.StatusUnprocessableEntity, "provider_error", clientSafeGatewayMsg(pe.Message))
			return
		}
		slog.Warn("card charge: falha no provider", "err", err)
		writeError(w, http.StatusUnprocessableEntity, "provider_error", "Falha ao processar o cartão. Tente novamente.")
		return
	}

	// Persiste a cobrança localmente.
	desc := in.Description
	if desc == "" {
		desc = "Cobrança com cartão"
	}
	c := &Charge{
		Kind:             "avulso",
		CustomerID:       in.CustomerID,
		AmountCents:      in.AmountCents,
		DueDate:          time.Now().Format("2006-01-02"),
		Provider:         "efi",
		Method:           "card",
		CorrelationID:    newCorrelationID(),
		ProviderChargeID: result.ChargeID,
		PublicToken:      newPublicToken(),
		payerTaxID:       onlyDigits(in.Customer.TaxID),
	}

	if s.store != nil {
		if err := s.store.InsertCharge(r.Context(), c); err != nil {
			slog.Warn("card charge: falha ao gravar cobrança", "err", err)
			writeError(w, http.StatusInternalServerError, "db_error", "Falha ao registrar a cobrança")
			return
		}
		// Se aprovado sincronamente, marca paga de imediato.
		if result.Status == "paid" {
			if err := s.store.MarkChargePaid(r.Context(), c.CorrelationID); err != nil {
				slog.Warn("card charge: falha ao marcar paga", "err", err)
			}
		}
	}

	c.Status = result.Status
	c.PayerName = in.Customer.Name

	// Status unpaid: cartão recusado — informa o motivo (sem bloquear retry).
	if result.Status == "unpaid" {
		c.Refusal = &CardRefusalInfo{Reason: result.Message, Retry: true}
	}

	_ = desc // reservado para futuros items com description
	writeJSON(w, http.StatusCreated, c)
}

// handleGetInstallments (GET /installments) consulta as parcelas disponíveis
// para uma bandeira e valor total.
func (s *Server) handleGetInstallments(w http.ResponseWriter, r *http.Request) {
	if s.efiCobr == nil {
		writeError(w, http.StatusServiceUnavailable, "card_unavailable", "Pagamento com cartão indisponível no momento")
		return
	}
	brand := r.URL.Query().Get("brand")
	if brand == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "brand é obrigatório")
		return
	}
	totalStr := r.URL.Query().Get("total")
	total, err := strconv.Atoi(totalStr)
	if err != nil || total <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "total (em centavos) deve ser um inteiro positivo")
		return
	}
	installments, err := s.efiCobr.Installments(r.Context(), brand, total)
	if err != nil {
		slog.Warn("installments: falha na Efí", "err", err)
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao consultar parcelas na Efí")
		return
	}
	if installments == nil {
		installments = []Installment{}
	}
	writeJSON(w, http.StatusOK, installments)
}

// handleRefundCard (POST /charges/card/{id}/refund) solicita o estorno de uma
// cobrança de cartão. Requer admin.
func (s *Server) handleRefundCard(w http.ResponseWriter, r *http.Request) {
	if s.efiCobr == nil {
		writeError(w, http.StatusServiceUnavailable, "card_unavailable", "Pagamento com cartão indisponível no momento")
		return
	}

	chargeIDStr := r.PathValue("id")
	if chargeIDStr == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "ID da cobrança obrigatório")
		return
	}

	chargeIDInt, err := strconv.ParseInt(chargeIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "ID inválido")
		return
	}

	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Serviço de dados indisponível")
		return
	}
	providerChargeID := chargeIDStr // será substituído pelo provider_charge_id abaixo
	if s.store != nil {
		c, err := s.store.GetCharge(r.Context(), chargeIDInt)
		if err != nil || c == nil {
			writeError(w, http.StatusNotFound, "not_found", "Cobrança não encontrada")
			return
		}
		if c.Method != "card" {
			writeError(w, http.StatusConflict, "invalid_method", "Estorno disponível apenas para cobranças com cartão")
			return
		}
		if c.Status != "paid" {
			writeError(w, http.StatusConflict, "not_paid", "Somente cobranças pagas podem ser estornadas")
			return
		}
		providerChargeID = c.ProviderChargeID
	}

	// Valor parcial (opcional).
	var amountCents *int64
	var body struct {
		AmountCents *int64 `json:"amountCents"`
	}
	// Ignora erro de decodificação — corpo é opcional para estorno total.
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.AmountCents != nil && *body.AmountCents > 0 {
		amountCents = body.AmountCents
	}

	if err := s.efiCobr.RefundCard(r.Context(), providerChargeID, amountCents); err != nil {
		var pe *ProviderError
		if errors.As(err, &pe) {
			writeError(w, http.StatusUnprocessableEntity, "provider_error", clientSafeGatewayMsg(pe.Message))
			return
		}
		slog.Warn("refund card: falha no gateway", "err", err)
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao solicitar estorno na Efí")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
