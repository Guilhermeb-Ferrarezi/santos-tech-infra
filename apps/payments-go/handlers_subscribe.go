package main

import (
	"context"
	"errors"
	"fmt"
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
	// dataInicial do contrato deve ser FUTURA (a Efí recusa = data de criação). "Cobrar no
	// dia": começa no próximo due_day. "Cobrar na assinatura": amanhã (o quanto antes); o
	// débito da 1ª parcela é disparado pelo webhook ao aprovar (ver handleRecWebhook).
	var startDate string
	if p.ChargeOnSubscribe {
		startDate = time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	} else {
		startDate = nextDueDate(dueDay, time.Now()).Format("2006-01-02")
	}
	token := newPublicToken()
	rec := &Recurrence{
		ProductID:   &prodID,
		CustomerID:  &custID,
		PayerTaxID:  in.TaxID,
		PayerName:   in.Name,
		AmountCents: p.PriceCents,
		Periodicity: p.Periodicity,
		DueDay:      &dueDay,
		StartDate:   startDate,
		Journey:     2, // só autoriza; a Efí decide a jornada na hora do pagador autorizar
		PublicToken: token,
	}
	// Persiste primeiro (status 'pending_auth') para ter o ID p/ o contrato.
	if err := s.recs.CreateRecurrence(r.Context(), rec); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao criar assinatura")
		return
	}

	recRes, err := s.efi.CreateRecurrence(r.Context(), RecurrenceRequest{
		Contract:    "Assinatura #" + strconv.FormatInt(rec.ID, 10),
		Object:      p.Name,
		PayerName:   in.Name,
		PayerTaxID:  in.TaxID,
		AmountCents: p.PriceCents,
		Periodicity: p.Periodicity,
		StartDate:   startDate,
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

	// A tela acompanha a aprovação por SSE em /subscribe/{token}/events (sem cobrança
	// imediata: PIX Automático autoriza agora e debita conforme o modo do produto).
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": token, "brCode": recRes.BRCode, "qrCode": recRes.QRCode, "amountCents": p.PriceCents,
	})
}

// maybeChargeFirstInstallment dispara a 1ª cobrança recorrente logo após a aprovação, se o
// produto está marcado como "cobrar na assinatura" (charge_on_subscribe). Best-effort,
// idempotente por (recurrence_id, reference_month). A cobr (/v2/cobr) só foi exercitada em
// produção — se a Efí recusar cobrar antes da dataInicial, ajustar a data aqui após o teste real.
func (s *Server) maybeChargeFirstInstallment(ctx context.Context, rec *Recurrence) {
	if rec.ProductID == nil {
		return
	}
	p, err := s.store.GetProductByID(ctx, *rec.ProductID)
	if err != nil || !p.ChargeOnSubscribe {
		return
	}
	refMonth := time.Now().Format("2006-01")
	due := time.Now().Format("2006-01-02")
	txid := recurringTxid(rec.ID, refMonth)
	res, err := s.efi.CreateRecurringCharge(ctx, RecurringChargeRequest{
		CorrelationID: txid,
		EfiIDRec:      rec.EfiIDRec,
		AmountCents:   rec.AmountCents,
		PayerName:     rec.PayerName,
		PayerTaxID:    rec.PayerTaxID,
		DueDate:       due,
	})
	if err != nil {
		slog.Warn("assinatura: falha ao cobrar 1ª parcela na aprovação", "rec", rec.ID, "err", err)
		return
	}
	c := &Charge{
		RecurrenceID: &rec.ID, CustomerID: rec.CustomerID,
		AmountCents: rec.AmountCents, DueDate: due, ReferenceMonth: &refMonth,
		Provider: "efi", ProviderChargeID: res.ProviderChargeID,
		CorrelationID: txid, BRCode: res.BRCode, QRCode: res.QRCode,
		PublicToken: newPublicToken(),
	}
	c.payerTaxID = rec.PayerTaxID
	if err := s.store.InsertRecurrenceCharge(ctx, c); err != nil {
		slog.Warn("assinatura: falha ao gravar 1ª cobrança da aprovação", "rec", rec.ID, "err", err)
	}
}

// handleGetSubscribe devolve o snapshot do status da assinatura pelo token público (a tela
// usa pra renderizar o QR e o estado: pending_auth/active/rejected/canceled/expired).
func (s *Server) handleGetSubscribe(w http.ResponseWriter, r *http.Request) {
	rec, err := s.recs.GetRecurrenceByPublicToken(r.Context(), r.PathValue("token"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Assinatura não encontrada")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": rec.Status, "brCode": rec.BRCode, "qrCode": rec.QRCode, "amountCents": rec.AmountCents,
	})
}

// handleSubscribeEvents transmite por SSE a mudança de status da assinatura: manda o estado
// atual e, quando o webhookrec publicar a transição (active/rejected/canceled), empurra e
// encerra. Espelha handlePayEvents (cobrança), mas para a recorrência.
func (s *Server) handleSubscribeEvents(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	rec, err := s.recs.GetRecurrenceByPublicToken(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Assinatura não encontrada")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "sse_unsupported", "Streaming indisponível")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	fmt.Fprintf(w, "event: status\ndata: %s\n\n", rec.Status)
	flusher.Flush()
	if rec.Status != "pending_auth" {
		return // já resolvido
	}

	pubsub := s.subscribeRecurrence(r.Context(), token)
	defer pubsub.Close()
	ch := pubsub.Channel()

	// Re-checa após assinar: fecha a corrida em que a aprovação cai ENTRE o GET e o Subscribe.
	if r2, err := s.recs.GetRecurrenceByPublicToken(r.Context(), token); err == nil && r2.Status != "pending_auth" {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", r2.Status, r2.Status)
		flusher.Flush()
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			if msg != nil && msg.Payload != "" {
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.Payload, msg.Payload)
				flusher.Flush()
				return
			}
		}
	}
}
