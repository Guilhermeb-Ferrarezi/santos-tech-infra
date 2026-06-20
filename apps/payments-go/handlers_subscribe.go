package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// subscribeStore isola o acesso ao banco usado pelo checkout recorrente (POST
// /me/subscribe): produto, cliente e a 1ª cobrança do ciclo. O *Store satisfaz em
// prod; um fake nos testes (sem Postgres). A recorrência em si passa pelo s.recs.
type subscribeStore interface {
	GetProductByID(ctx context.Context, id int64) (*Product, error)
	UpsertCustomer(ctx context.Context, userID int64, taxID, phone, name, email string) (*Customer, error)
	InsertRecurrenceCharge(ctx context.Context, c *Charge) error
}

// handleSubscribe (sessão) é o checkout de um produto recorrente (assinatura). Espelha
// handleCheckout, mas para um item único via PIX Automático Jornada 3: cria a recorrência
// + a 1ª cobrança (vencimento hoje) num ÚNICO QR de autorização+pagamento. O contrato
// nasce 'pending_auth'; o webhookrec move para 'active' quando o pagador autoriza e o
// webhook pix marca a 1ª pay_charges como 'paid' (por correlation_id).
func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProductID int64  `json:"productId"`
		TaxID     string `json:"taxId"`
		Phone     string `json:"phone"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		// Save controla apenas o pré-preenchimento no frontend (localStorage).
		Save bool `json:"save"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	in.TaxID = onlyDigits(in.TaxID)
	in.Phone = onlyDigits(in.Phone)
	in.Name = strings.TrimSpace(in.Name)
	in.Email = strings.TrimSpace(in.Email)
	if in.ProductID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "productId obrigatório")
		return
	}
	if !validName(in.Name) {
		writeError(w, http.StatusBadRequest, "invalid_body", "Nome obrigatório (máximo 100 caracteres)")
		return
	}
	if !validEmail(in.Email) {
		writeError(w, http.StatusBadRequest, "invalid_body", "E-mail inválido")
		return
	}
	if !validCPF(in.TaxID) {
		writeError(w, http.StatusBadRequest, "invalid_body", "CPF inválido (11 dígitos)")
		return
	}
	if !validPhone(in.Phone) {
		writeError(w, http.StatusBadRequest, "invalid_body", "Telefone inválido (10 ou 11 dígitos)")
		return
	}

	// Produto precisa existir e ser recorrente (senão é o checkout normal).
	p, err := s.subs.GetProductByID(r.Context(), in.ProductID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Produto não encontrado")
		return
	}
	if !p.Recurring {
		writeError(w, http.StatusConflict, "not_recurring", "Produto não é uma assinatura")
		return
	}
	// Defesa em profundidade: um produto marcado recorrente sem periodicity/due_day
	// válidos não pode virar contrato (a validação do produto já garante, mas o checkout
	// não confia cegamente no banco).
	if !validPeriodicities[p.Periodicity] || p.DueDay == nil || *p.DueDay < 1 || *p.DueDay > 28 {
		writeError(w, http.StatusConflict, "invalid_product", "Produto recorrente mal configurado")
		return
	}

	uid := s.uid(r)
	// Cliente único por (conta, CPF), igual ao checkout avulso.
	cust, err := s.subs.UpsertCustomer(r.Context(), uid, in.TaxID, in.Phone, in.Name, in.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha no cliente")
		return
	}

	custID := cust.ID
	prodID := p.ID
	dueDay := *p.DueDay
	today := time.Now().Format("2006-01-02")
	// dataInicial do contrato deve ser FUTURA (a Efí recusa = data de criação). Na Jornada 3
	// a 1ª parcela é a cob imediata (hoje); o contrato começa no próximo ciclo.
	cycleStart := nextCycleDate(p.Periodicity, time.Now()).Format("2006-01-02")
	rec := &Recurrence{
		ProductID:   &prodID,
		CustomerID:  &custID,
		PayerTaxID:  in.TaxID,
		PayerName:   in.Name,
		AmountCents: p.PriceCents,
		Periodicity: p.Periodicity,
		DueDay:      &dueDay,
		StartDate:   cycleStart,
		Journey:     3,
	}
	// Persiste primeiro (status 'pending_auth') para ter o ID — também ancora o txid
	// determinístico da 1ª cobr (recurringTxid), tornando o PUT /v2/cob idempotente.
	if err := s.recs.CreateRecurrence(r.Context(), rec); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao criar assinatura")
		return
	}

	refMonth := time.Now().Format("2006-01")
	txid := recurringTxid(rec.ID, refMonth)
	recRes, charge, err := s.efi.CreateRecurrenceJornada3(r.Context(), RecurrenceJornada3Request{
		Contract:            "Assinatura #" + strconv.FormatInt(rec.ID, 10),
		Object:              p.Name,
		PayerName:           in.Name,
		PayerTaxID:          in.TaxID,
		AmountCents:         p.PriceCents,
		Periodicity:         p.Periodicity,
		StartDate:           cycleStart, // contrato começa no próximo ciclo (1ª parcela = cob imediata)
		ChargeCorrelationID: txid,
		FirstDueDate:        today, // a cob imediata vence hoje
		Description:         "Assinatura " + p.Name,
	})
	if err != nil {
		// 422 (não 502): Cloudflare/Traefik troca 5xx do origin por HTML sem CORS.
		_ = s.recs.SetRecurrenceStatus(r.Context(), rec.ID, "rejected")
		var pe *ProviderError
		if errors.As(err, &pe) {
			slog.Warn("assinatura: erro do gateway", "status", pe.Status, "message", pe.Message)
			writeError(w, http.StatusUnprocessableEntity, "provider_error", clientSafeGatewayMsg(pe.Message))
			return
		}
		slog.Warn("assinatura: falha no provider", "err", err)
		writeError(w, http.StatusUnprocessableEntity, "provider_error", "Falha ao criar a assinatura. Tente novamente.")
		return
	}
	if err := s.recs.UpdateRecurrenceAuth(r.Context(), rec.ID, recRes.EfiIDRec, recRes.BRCode, recRes.QRCode, recRes.Status); err != nil {
		slog.Warn("assinatura: falha ao gravar autorização", "rec", rec.ID, "err", err)
	}

	// 1ª cobrança do ciclo (kind='recorrente'): public_token p/ a tela /pay/{token},
	// correlation_id = txid (casa com o webhook pix do débito).
	ref := refMonth
	c := &Charge{
		RecurrenceID:     &rec.ID,
		CustomerID:       &custID,
		AmountCents:      p.PriceCents,
		DueDate:          today,
		ReferenceMonth:   &ref,
		Provider:         "efi",
		ProviderChargeID: charge.ProviderChargeID,
		CorrelationID:    txid,
		PublicToken:      newPublicToken(),
		BRCode:           recRes.BRCode,
		QRCode:           recRes.QRCode,
	}
	c.payerTaxID = in.TaxID
	if err := s.subs.InsertRecurrenceCharge(r.Context(), c); err != nil {
		// A recorrência+cobr já existem na Efí; registra para reconciliação e segue.
		slog.Warn("assinatura: falha ao gravar 1ª cobrança", "rec", rec.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao registrar a cobrança")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"token": c.PublicToken, "brCode": recRes.BRCode, "qrCode": recRes.QRCode, "amountCents": p.PriceCents,
	})
}
