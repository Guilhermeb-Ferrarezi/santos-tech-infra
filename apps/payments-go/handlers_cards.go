package main

import (
	"context"
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

// hasSensitiveCardFieldsDeep verifica apenas chaves inequivocamente de PAN/CVV em
// qualquer nível de aninhamento, SEM checar campos raiz desconhecidos.
// Use esta versão quando o body não segue o schema de CardChargeInput (ex: payLinkInput).
func hasSensitiveCardFieldsDeep(raw []byte) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	return checkSensitiveDeep(v)
}

func checkSensitiveDeep(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			kl := strings.ToLower(k)
			for _, f := range panCVVFieldsDeep {
				if kl == f {
					return true
				}
			}
			if checkSensitiveDeep(child) {
				return true
			}
		}
	case []any:
		for _, item := range t {
			if checkSensitiveDeep(item) {
				return true
			}
		}
	}
	return false
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
			// Não loga pe.Message bruto — pode conter dados do gateway com info sensível.
			slog.Warn("card charge: erro do gateway", "status", pe.Status)
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

// allowedCardBrands são as bandeiras aceitas pela API Cobranças Efí.
var allowedCardBrands = map[string]bool{
	"visa":       true,
	"mastercard": true,
	"elo":        true,
	"amex":       true,
	"hipercard":  true,
}

// handleGetInstallments (GET /installments) consulta as parcelas disponíveis
// para uma bandeira e valor total.
func (s *Server) handleGetInstallments(w http.ResponseWriter, r *http.Request) {
	if s.efiCobr == nil {
		writeError(w, http.StatusServiceUnavailable, "card_unavailable", "Pagamento com cartão indisponível no momento")
		return
	}
	brand := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("brand")))
	if brand == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "brand é obrigatório")
		return
	}
	if !allowedCardBrands[brand] {
		writeError(w, http.StatusBadRequest, "invalid_brand", "Bandeira não suportada. Use: visa, mastercard, elo, amex ou hipercard")
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

// chargeRefundStore isola as operações de banco necessárias para handleRefundCard.
type chargeRefundStore interface {
	GetCharge(ctx context.Context, id int64) (*Charge, error)
	// BeginChargeRefund reserva a cobrança ('paid' → 'refunding') numa única
	// instrução. false = outra requisição já reservou, ou a cobrança não está paga.
	// Nunca volte ao check-then-act: ele deixava dois estornos simultâneos passarem.
	BeginChargeRefund(ctx context.Context, correlationID string) (bool, error)
	// RollbackChargeRefund devolve a cobrança a 'paid' quando o gateway recusa.
	RollbackChargeRefund(ctx context.Context, correlationID string) error
	// FinishChargeRefund soma o valor estornado e resolve o status final
	// ('paid' enquanto for parcial, 'refunded' quando o acumulado fecha o valor).
	FinishChargeRefund(ctx context.Context, correlationID string, refundedCents int64) (string, error)
}

// refundStoreOf devolve o store de estorno: usa s.refund quando injetado (testes),
// e cai para s.store em produção.
func (s *Server) refundStoreOf() chargeRefundStore {
	if s.refund != nil {
		return s.refund
	}
	if s.store != nil {
		return s.store
	}
	return nil
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

	rs := s.refundStoreOf()
	if rs == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Serviço de dados indisponível")
		return
	}

	c, err := rs.GetCharge(r.Context(), chargeIDInt)
	if err != nil || c == nil {
		writeError(w, http.StatusNotFound, "not_found", "Cobrança não encontrada")
		return
	}
	if c.Method != "card" {
		writeError(w, http.StatusConflict, "invalid_method", "Estorno disponível apenas para cobranças com cartão")
		return
	}
	if c.Status == "refunded" {
		writeError(w, http.StatusConflict, "already_refunded", "Esta cobrança já foi estornada")
		return
	}
	if c.Status != "paid" {
		writeError(w, http.StatusConflict, "not_paid", "Somente cobranças pagas podem ser estornadas")
		return
	}
	if c.ProviderChargeID == "" {
		writeError(w, http.StatusConflict, "no_provider_id", "Cobrança sem ID do gateway; estorno manual necessário")
		return
	}
	providerChargeID := c.ProviderChargeID

	// Valor parcial (opcional). O saldo estornável é o que ainda não foi devolvido.
	remaining := c.AmountCents - c.RefundedCents
	if remaining <= 0 {
		writeError(w, http.StatusConflict, "already_refunded", "Esta cobrança já foi estornada")
		return
	}
	var amountCents *int64
	var body struct {
		AmountCents *int64 `json:"amountCents"`
	}
	// Ignora erro de decodificação — corpo é opcional para estorno total.
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.AmountCents != nil {
		// Teto obrigatório: sem ele dava para pedir à Efí um estorno maior do que a
		// cobrança — devolvendo dinheiro que nunca entrou.
		if *body.AmountCents <= 0 || *body.AmountCents > remaining {
			writeError(w, http.StatusBadRequest, "invalid_amount", "amountCents deve estar entre 1 e o saldo ainda não estornado")
			return
		}
		amountCents = body.AmountCents
	}
	// Valor efetivamente estornado nesta chamada (sem corpo = estorno total do saldo).
	refunding := remaining
	if amountCents != nil {
		refunding = *amountCents
	}

	// RESERVA antes do gateway: 'paid' → 'refunding' numa única instrução. Duas
	// requisições simultâneas — só uma reserva, só uma estorna.
	reserved, err := rs.BeginChargeRefund(r.Context(), c.CorrelationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao reservar o estorno")
		return
	}
	if !reserved {
		writeError(w, http.StatusConflict, "refund_in_progress", "Já existe um estorno em andamento para esta cobrança")
		return
	}

	if err := s.efiCobr.RefundCard(r.Context(), providerChargeID, amountCents); err != nil {
		// Gateway recusou: devolve a cobrança a 'paid' para não travar num estado
		// intermediário e permitir uma nova tentativa.
		if rbErr := rs.RollbackChargeRefund(context.WithoutCancel(r.Context()), c.CorrelationID); rbErr != nil {
			slog.Error("refund card: falha ao desfazer a reserva do estorno", "charge", c.ID, "err", rbErr)
		}
		var pe *ProviderError
		if errors.As(err, &pe) {
			// Não loga pe.Message bruto — pode conter dados sensíveis do gateway.
			slog.Warn("refund card: erro do gateway", "status", pe.Status)
			writeError(w, http.StatusUnprocessableEntity, "provider_error", clientSafeGatewayMsg(pe.Message))
			return
		}
		slog.Warn("refund card: falha no gateway", "err", err)
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao solicitar estorno na Efí")
		return
	}

	// Registra o valor estornado. Parcial volta para 'paid'; só o total vira 'refunded'.
	status, err := rs.FinishChargeRefund(context.WithoutCancel(r.Context()), c.CorrelationID, refunding)
	if err != nil {
		// O estorno JÁ saiu no gateway — não devolve erro ao cliente, mas isto precisa
		// ser visível: o banco ficou dessincronizado do dinheiro.
		slog.Error("refund card: estorno executado mas não registrado no banco",
			"charge", c.ID, "correlation", c.CorrelationID, "refunded_cents", refunding, "err", err)
		status = c.Status
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"refundedCents":  refunding,
		"status":         status,
		"remainingCents": remaining - refunding,
	})
}
