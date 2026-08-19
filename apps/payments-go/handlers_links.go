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

	"github.com/jackc/pgx/v5"
)

// linkStoreOf devolve s.links se configurado (injeção de teste), senão s.store.
func (s *Server) linkStoreOf() paymentLinkStore {
	if s.links != nil {
		return s.links
	}
	if s.store != nil {
		return s.store
	}
	return nil
}

// validLinkMethod verifica se um método é suportado em links de pagamento.
func validLinkMethod(m string) bool {
	return m == "pix" || m == "card" || m == "boleto"
}

// createLinkInput é o corpo de POST /payment-links.
type createLinkInput struct {
	AmountCents *int64   `json:"amountCents"` // nil = valor livre
	ProductIDs  []int    `json:"productIds"`
	Methods     []string `json:"methods"` // pix | card | boleto
	Coupons     []string `json:"coupons"`
	FinishURL   string   `json:"finishUrl"`
	ReturnURL   string   `json:"returnUrl"`
}

// handleCreatePaymentLink (POST /payment-links) cria um link reutilizável.
// Requer admin (role 3).
func (s *Server) handleCreatePaymentLink(w http.ResponseWriter, r *http.Request) {
	var in createLinkInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}

	// Valida métodos.
	if len(in.Methods) == 0 {
		in.Methods = []string{"pix"}
	}
	for _, m := range in.Methods {
		if !validLinkMethod(m) {
			writeError(w, http.StatusBadRequest, "invalid_body", "Método inválido: "+m+" (use pix, card ou boleto)")
			return
		}
	}

	// Valor não pode ser negativo se informado.
	if in.AmountCents != nil && *in.AmountCents <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "amountCents deve ser > 0 quando informado")
		return
	}

	if in.ProductIDs == nil {
		in.ProductIDs = []int{}
	}
	if in.Coupons == nil {
		in.Coupons = []string{}
	}

	st := s.linkStoreOf()
	if st == nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Store indisponível")
		return
	}

	l := &PaymentLink{
		PublicToken: newPublicToken(),
		AmountCents: in.AmountCents,
		ProductIDs:  in.ProductIDs,
		Methods:     in.Methods,
		Coupons:     in.Coupons,
		FinishURL:   strings.TrimSpace(in.FinishURL),
		ReturnURL:   strings.TrimSpace(in.ReturnURL),
	}

	if err := st.CreatePaymentLink(r.Context(), l); err != nil {
		slog.Warn("payment link: falha ao criar", "err", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao criar o link de pagamento")
		return
	}

	writeJSON(w, http.StatusCreated, l)
}

// handleListPaymentLinks (GET /payment-links) lista todos os links. Requer admin.
func (s *Server) handleListPaymentLinks(w http.ResponseWriter, r *http.Request) {
	st := s.linkStoreOf()
	if st == nil {
		writeJSON(w, http.StatusOK, []PaymentLink{})
		return
	}
	list, err := st.ListPaymentLinks(r.Context())
	if err != nil {
		slog.Warn("payment links: falha ao listar", "err", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar links")
		return
	}
	if list == nil {
		list = []PaymentLink{}
	}
	writeJSON(w, http.StatusOK, list)
}

// handleGetPaymentLink (GET /payment-links/{id}) retorna um link por ID. Requer admin.
func (s *Server) handleGetPaymentLink(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "ID inválido")
		return
	}
	st := s.linkStoreOf()
	if st == nil {
		writeError(w, http.StatusNotFound, "not_found", "Link não encontrado")
		return
	}
	l, err := st.GetPaymentLink(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "Link não encontrado")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao buscar link")
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// patchLinkInput é o corpo de PATCH /payment-links/{id}.
type patchLinkInput struct {
	Status string `json:"status"`
}

// handlePatchPaymentLink (PATCH /payment-links/{id}) altera o status de um link.
// Requer admin.
func (s *Server) handlePatchPaymentLink(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "ID inválido")
		return
	}
	var in patchLinkInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	if in.Status != "active" && in.Status != "inactive" {
		writeError(w, http.StatusBadRequest, "invalid_body", "status deve ser 'active' ou 'inactive'")
		return
	}
	st := s.linkStoreOf()
	if st == nil {
		writeError(w, http.StatusNotFound, "not_found", "Link não encontrado")
		return
	}
	if err := st.SetPaymentLinkStatus(r.Context(), id, in.Status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "Link não encontrado")
			return
		}
		slog.Warn("payment link: falha ao alterar status", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao atualizar status do link")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": in.Status})
}

// handleGetLinkByToken (GET /link/{token}) resolve um link público para o checkout.
// Rota PÚBLICA — sem guard de autenticação.
func (s *Server) handleGetLinkByToken(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "token obrigatório")
		return
	}
	st := s.linkStoreOf()
	if st == nil {
		writeError(w, http.StatusNotFound, "not_found", "Link não encontrado")
		return
	}
	l, err := st.GetPaymentLinkByToken(r.Context(), token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "Link não encontrado")
			return
		}
		slog.Warn("link checkout: falha ao buscar link", "token", token, "err", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao resolver o link")
		return
	}
	if l.Status != "active" {
		writeError(w, http.StatusGone, "link_inactive", "Este link de pagamento está inativo")
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// payLinkInput é o corpo de POST /link/{token}/pay.
// O pagador escolhe o método e fornece os dados de pagamento não-sensíveis.
type payLinkInput struct {
	Method         string             `json:"method"`         // pix | card | boleto
	AmountCents    *int64             `json:"amountCents"`    // obrigatório quando link tem valor livre
	PaymentToken   string             `json:"paymentToken"`   // para card: token gerado pelo SDK Efí
	Installments   int                `json:"installments"`   // para card: número de parcelas
	Customer       CardCustomer       `json:"customer"`       // nome + CPF (obrigatório p/ boleto/card)
	BillingAddress CardBillingAddress `json:"billingAddress"` // obrigatório para card
	DueDate        string             `json:"dueDate"`        // YYYY-MM-DD; default hoje+3 para boleto
	Coupon         string             `json:"coupon"`         // código do cupom (opcional)
}

// handlePayViaLink (POST /link/{token}/pay) cria uma charge vinculada ao link.
// Rota PÚBLICA — sem guard de autenticação.
// Guards: rate-limit por IP (5 req/min, process-level) e inspeção PCI de PAN/CVV.
func (s *Server) handlePayViaLink(w http.ResponseWriter, r *http.Request) {
	// ── Rate-limit por IP (process-level; ver deferred para Redis) ──────────
	ip := clientIP(r)
	var linkLimiter rateLimiterIface
	if s.linkPayRateLimiter != nil {
		linkLimiter = s.linkPayRateLimiter
	} else {
		linkLimiter = payLinkLimiterFor(ip)
	}
	if !linkLimiter.Allow() {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Muitas requisições. Aguarde e tente novamente.")
		return
	}

	token := r.PathValue("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "token obrigatório")
		return
	}

	st := s.linkStoreOf()
	if st == nil {
		writeError(w, http.StatusNotFound, "not_found", "Link não encontrado")
		return
	}

	l, err := st.GetPaymentLinkByToken(r.Context(), token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "Link não encontrado")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao resolver o link")
		return
	}
	if l.Status != "active" {
		writeError(w, http.StatusGone, "link_inactive", "Este link de pagamento está inativo")
		return
	}

	// ── Guard PCI: inspeção de PAN/CVV ANTES do decode (idêntico ao handleCreateCardCharge) ──
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Corpo inválido")
		return
	}
	if hasSensitiveCardFieldsDeep(raw) {
		slog.Warn("handlePayViaLink: corpo rejeitado por conter campos sensíveis de cartão",
			"link", l.ID, "ip", ip)
		writeError(w, http.StatusBadRequest, "sensitive_fields", "O corpo da requisição não deve conter número de cartão ou CVV. Use payment_token.")
		return
	}

	var in payLinkInput
	if err := json.Unmarshal(raw, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}

	// Valida método: deve estar na lista do link.
	if !validLinkMethod(in.Method) {
		writeError(w, http.StatusBadRequest, "invalid_body", "Método inválido: "+in.Method)
		return
	}
	methodAllowed := false
	for _, m := range l.Methods {
		if m == in.Method {
			methodAllowed = true
			break
		}
	}
	if !methodAllowed {
		writeError(w, http.StatusBadRequest, "method_not_allowed", "Este link não aceita o método "+in.Method)
		return
	}

	// Resolve o valor: link com valor fixo prevalece; valor livre requer amountCents no body.
	var amountCents int64
	if l.AmountCents != nil {
		amountCents = *l.AmountCents
	} else {
		if in.AmountCents == nil || *in.AmountCents <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_body", "amountCents obrigatório para links com valor livre")
			return
		}
		// Teto de valor (configurável via LINK_MAX_CENTS, default R$ 10.000).
		maxCents := s.cfg.LinkMaxCents
		if maxCents <= 0 {
			maxCents = 1_000_000
		}
		if *in.AmountCents > maxCents {
			writeError(w, http.StatusBadRequest, "amount_too_large", "Valor excede o teto permitido para links de valor livre")
			return
		}
		amountCents = *in.AmountCents
	}

	// ── Cupom (opcional) ─────────────────────────────────────────────────────
	// O uso é RESERVADO aqui, atomicamente, ANTES de falar com a Efí. Antes o código
	// lia max_uses, ia ao gateway e só então incrementava (com o erro descartado):
	// pagadores simultâneos furavam o limite do cupom.
	var appliedCouponID int64
	couponCommitted := false
	if in.Coupon != "" {
		cst := s.couponStoreOf()
		if cst == nil {
			writeError(w, http.StatusBadRequest, "invalid_coupon", "Cupom inválido")
			return
		}
		// A lista de cupons do link é FILTRO, não sugestão visual. Antes qualquer
		// cupom ativo valia em qualquer link — um cupom de 90% criado para um curso
		// barato valia num link de valor livre de R$ 10.000. Checado ANTES do resgate
		// para não queimar um uso do cupom à toa.
		if !couponAllowedOnLink(l, in.Coupon) {
			writeError(w, http.StatusBadRequest, "invalid_coupon", "Cupom inválido ou esgotado")
			return
		}
		coup, err := cst.RedeemCoupon(r.Context(), in.Coupon)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_coupon", "Cupom inválido ou esgotado")
			return
		}
		amountCents -= s.couponDiscountFor(coup, amountCents)
		if amountCents < 0 {
			amountCents = 0
		}
		appliedCouponID = coup.ID
		// Pagamento que não vinga devolve o uso reservado.
		defer func() {
			if !couponCommitted {
				s.releaseCoupon(r.Context(), appliedCouponID)
			}
		}()
	}

	// Método card: requer payment_token.
	if in.Method == "card" {
		if s.efiCobr == nil {
			writeError(w, http.StatusServiceUnavailable, "card_unavailable", "Pagamento com cartão indisponível")
			return
		}
		if in.PaymentToken == "" {
			writeError(w, http.StatusBadRequest, "invalid_body", "paymentToken obrigatório para pagamento com cartão")
			return
		}
		if in.Customer.Name == "" || in.Customer.TaxID == "" {
			writeError(w, http.StatusBadRequest, "invalid_body", "customer.name e customer.taxId obrigatórios")
			return
		}

		result, err := s.efiCobr.ChargeCardOneStep(r.Context(), CardChargeInput{
			PaymentToken:   in.PaymentToken,
			Installments:   in.Installments,
			AmountCents:    amountCents,
			Customer:       in.Customer,
			BillingAddress: in.BillingAddress,
		})
		if err != nil {
			var pe *ProviderError
			if errors.As(err, &pe) {
				slog.Warn("pay via link (card): erro do gateway", "link", l.ID, "status", pe.Status)
				writeError(w, http.StatusUnprocessableEntity, "provider_error", clientSafeGatewayMsg(pe.Message))
				return
			}
			slog.Warn("pay via link (card): falha no provider", "link", l.ID, "err", err)
			writeError(w, http.StatusUnprocessableEntity, "provider_error", "Falha ao processar o cartão. Tente novamente.")
			return
		}

		c := &Charge{
			Kind:             "avulso",
			AmountCents:      amountCents,
			DueDate:          time.Now().Format("2006-01-02"),
			Provider:         "efi",
			Method:           "card",
			CorrelationID:    newCorrelationID(),
			ProviderChargeID: result.ChargeID,
			PublicToken:      newPublicToken(),
			payerTaxID:       onlyDigits(in.Customer.TaxID),
		}

		if err := st.InsertChargeWithLink(r.Context(), c, l.ID); err != nil {
			slog.Warn("pay via link (card): falha ao gravar cobrança", "link", l.ID, "err", err)
			writeError(w, http.StatusInternalServerError, "db_error", "Falha ao registrar a cobrança")
			return
		}
		couponCommitted = true // cobrança criada: o uso reservado do cupom fica de pé
		if result.Status == "paid" {
			_ = st.MarkChargePaid(r.Context(), c.CorrelationID)
		}

		c.Status = result.Status
		c.PayerName = in.Customer.Name
		c.LinkID = &l.ID
		if result.Status == "unpaid" {
			c.Refusal = &CardRefusalInfo{Reason: result.Message, Retry: true}
		}
		writeJSON(w, http.StatusCreated, c)
		return
	}

	// Método boleto.
	if in.Method == "boleto" {
		if s.efiCobr == nil {
			writeError(w, http.StatusServiceUnavailable, "boleto_unavailable", "Boleto indisponível no momento")
			return
		}
		if in.Customer.Name == "" || in.Customer.TaxID == "" {
			writeError(w, http.StatusBadRequest, "invalid_body", "customer.name e customer.taxId obrigatórios para boleto")
			return
		}
		dueDate := strings.TrimSpace(in.DueDate)
		if dueDate == "" {
			dueDate = time.Now().AddDate(0, 0, 3).Format("2006-01-02")
		} else {
			if d, err := time.Parse("2006-01-02", dueDate); err != nil || d.Before(time.Now().Truncate(24*time.Hour)) {
				writeError(w, http.StatusBadRequest, "invalid_body", "dueDate deve ser YYYY-MM-DD, hoje ou futura")
				return
			}
		}

		notifyURL := ""
		if s.cfg.EFIWebhookURL != "" {
			notifyURL = s.cfg.EFIWebhookURL + "/cobr"
			if s.cfg.EFIWebhookSecret != "" {
				notifyURL += "?token=" + s.cfg.EFIWebhookSecret
			}
		}
		correlationID := newCorrelationID()
		res, err := s.efiCobr.CreateBoleto(r.Context(), BoletoRequest{
			AmountCents:     amountCents,
			PayerName:       in.Customer.Name,
			PayerTaxID:      onlyDigits(in.Customer.TaxID),
			DueDate:         dueDate,
			CustomID:        correlationID,
			NotificationURL: notifyURL,
		})
		if err != nil {
			var pe *ProviderError
			if errors.As(err, &pe) {
				slog.Warn("pay via link (boleto): erro do gateway", "link", l.ID, "status", pe.Status)
				writeError(w, http.StatusUnprocessableEntity, "provider_error", clientSafeGatewayMsg(pe.Message))
				return
			}
			slog.Warn("pay via link (boleto): falha no provider", "link", l.ID, "err", err)
			writeError(w, http.StatusUnprocessableEntity, "provider_error", "Falha ao gerar o boleto. Tente novamente.")
			return
		}

		c := &Charge{
			Kind:             "avulso",
			AmountCents:      amountCents,
			DueDate:          dueDate,
			Provider:         "efi",
			Method:           "boleto",
			CorrelationID:    correlationID,
			ProviderChargeID: res.ChargeID,
			PublicToken:      newPublicToken(),
			BRCode:           res.Line,
			PDFURL:           res.PDFURL,
			Barcode:          res.Line,
			payerTaxID:       onlyDigits(in.Customer.TaxID),
		}

		if err := st.InsertChargeWithLink(r.Context(), c, l.ID); err != nil {
			slog.Warn("pay via link (boleto): falha ao gravar cobrança", "link", l.ID, "err", err)
			writeError(w, http.StatusInternalServerError, "db_error", "Falha ao registrar o boleto")
			return
		}
		couponCommitted = true // cobrança criada: o uso reservado do cupom fica de pé

		c.Status = res.Status
		c.PayerName = in.Customer.Name
		c.LinkID = &l.ID
		writeJSON(w, http.StatusCreated, c)
		return
	}

	// Método pix (padrão).
	if in.Customer.Name == "" || in.Customer.TaxID == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "customer.name e customer.taxId obrigatórios para pix")
		return
	}
	if s.provider == nil {
		writeError(w, http.StatusServiceUnavailable, "pix_unavailable", "PIX indisponível no momento")
		return
	}

	dueDate := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	expiresAt := time.Now().AddDate(0, 0, 3).Add(23 * time.Hour).Format(time.RFC3339)
	correlationID := newCorrelationID()

	pixRes, err := s.provider.CreateCharge(r.Context(), ChargeRequest{
		CorrelationID: correlationID,
		AmountCents:   amountCents,
		PayerName:     in.Customer.Name,
		PayerTaxID:    onlyDigits(in.Customer.TaxID),
		Description:   "Pagamento via link",
		ExpiresAt:     expiresAt,
	})
	if err != nil {
		var pe *ProviderError
		if errors.As(err, &pe) {
			slog.Warn("pay via link (pix): erro do gateway", "link", l.ID, "status", pe.Status)
			writeError(w, http.StatusUnprocessableEntity, "provider_error", clientSafeGatewayMsg(pe.Message))
			return
		}
		slog.Warn("pay via link (pix): falha no provider", "link", l.ID, "err", err)
		writeError(w, http.StatusUnprocessableEntity, "provider_error", "Falha ao gerar o PIX. Tente novamente.")
		return
	}
	if pixRes.CorrelationID != "" {
		correlationID = pixRes.CorrelationID
	}

	c := &Charge{
		Kind:             "avulso",
		AmountCents:      amountCents,
		DueDate:          dueDate,
		Provider:         "efi",
		Method:           "pix",
		CorrelationID:    correlationID,
		ProviderChargeID: pixRes.ProviderChargeID,
		PublicToken:      newPublicToken(),
		BRCode:           pixRes.BRCode,
		QRCode:           pixRes.QRCode,
		payerTaxID:       onlyDigits(in.Customer.TaxID),
	}

	if err := st.InsertChargeWithLink(r.Context(), c, l.ID); err != nil {
		slog.Warn("pay via link (pix): falha ao gravar cobrança", "link", l.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao registrar o PIX")
		return
	}
	couponCommitted = true // cobrança criada: o uso reservado do cupom fica de pé

	if c.QRCode == "" {
		c.QRCode = qrPNGDataURI(c.BRCode)
	}
	c.PayerName = in.Customer.Name
	c.LinkID = &l.ID
	writeJSON(w, http.StatusCreated, c)
}
