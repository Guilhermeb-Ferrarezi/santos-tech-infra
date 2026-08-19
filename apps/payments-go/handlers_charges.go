package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func newCorrelationID() string {
	b := make([]byte, 14)
	_, _ = rand.Read(b)
	return "stpay" + hex.EncodeToString(b) // 5 + 28 = 33 chars alnum (txid Efí: 26–35)
}

func newPublicToken() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type createChargeInput struct {
	Kind        string `json:"kind"` // matricula | avulso
	StudentID   int64  `json:"studentId"`
	AmountCents int64  `json:"amountCents"`
	Description string `json:"description"`
	DueDate     string `json:"dueDate"` // YYYY-MM-DD; default hoje+3
}

func (s *Server) handleCreateCharge(w http.ResponseWriter, r *http.Request) {
	var in createChargeInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	if in.Kind != "matricula" && in.Kind != "avulso" {
		writeError(w, http.StatusBadRequest, "invalid_body", "kind deve ser matricula|avulso")
		return
	}
	if in.StudentID == 0 || in.AmountCents <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "studentId e amountCents>0 obrigatórios")
		return
	}
	st, err := s.store.GetStudent(r.Context(), in.StudentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Aluno não encontrado")
		return
	}
	if in.DueDate == "" {
		in.DueDate = time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	} else {
		d, err := time.Parse("2006-01-02", in.DueDate)
		if err != nil || d.Before(time.Now().Truncate(24*time.Hour)) {
			writeError(w, http.StatusBadRequest, "invalid_body", "dueDate deve ser uma data válida no formato YYYY-MM-DD, hoje ou futura")
			return
		}
	}
	c := &Charge{
		Kind: in.Kind, StudentID: &st.ID, AmountCents: in.AmountCents,
		DueDate: in.DueDate, Provider: "efi", CorrelationID: newCorrelationID(),
	}
	if err := s.createAndPersistCharge(r.Context(), c, st, in.Description); err != nil {
		var pe *ProviderError
		if errors.As(err, &pe) {
			slog.Warn("charge: erro do gateway", "status", pe.Status, "message", pe.Message)
			writeError(w, http.StatusUnprocessableEntity, "provider_error", clientSafeGatewayMsg(pe.Message))
			return
		}
		slog.Warn("charge: falha no provider", "err", err)
		writeError(w, http.StatusUnprocessableEntity, "provider_error", "Falha ao gerar a cobrança. Tente novamente.")
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// manualPayerInput é a parte de pagador compartilhada pelo "Gerar PIX" e "Gerar assinatura"
// do dashboard: cliente existente (customerId) OU avulso (nome+CPF), com opção de salvar.
type manualPayerInput struct {
	CustomerID   int64  `json:"customerId"`   // cliente existente
	PayerName    string `json:"payerName"`    // avulso
	PayerTaxID   string `json:"payerTaxId"`   // avulso (CPF)
	PayerEmail   string `json:"payerEmail"`   // avulso (destino do email)
	Phone        string `json:"phone"`        // avulso (ao salvar como cliente)
	SendEmail    bool   `json:"sendEmail"`    // envia por email ao cliente
	SaveCustomer bool   `json:"saveCustomer"` // avulso: cria o cliente (consolida por CPF)
}

type manualChargeInput struct {
	manualPayerInput
	ProductID   int64  `json:"productId"` // opcional: registra o item a partir do catálogo
	AmountCents int64  `json:"amountCents"`
	Description string `json:"description"`
	DueDate     string `json:"dueDate"` // YYYY-MM-DD; default hoje+3
	Method      string `json:"method"`  // pix (default) | boleto
}

// resolveManualPayer resolve o pagador de uma cobrança/assinatura gerada no dashboard.
// Cliente existente → usa os dados dele. Avulso → valida nome+CPF; se o CPF já é de um
// cliente, trata como AQUELE cliente (dados canônicos, sem sobrescrever); senão cria o
// cliente quando SaveCustomer. Em falha, escreve o erro em w e devolve ok=false.
func (s *Server) resolveManualPayer(w http.ResponseWriter, r *http.Request, p manualPayerInput) (name, taxID, email string, customerID *int64, ok bool) {
	if p.CustomerID != 0 {
		cust, err := s.store.GetCustomerDetail(r.Context(), p.CustomerID)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "Cliente não encontrado")
			return "", "", "", nil, false
		}
		cid := cust.ID
		return cust.Name, cust.TaxID, cust.Email, &cid, true
	}
	name = strings.TrimSpace(p.PayerName)
	taxID = onlyDigits(p.PayerTaxID)
	email = strings.TrimSpace(p.PayerEmail)
	if !validName(name) {
		writeError(w, http.StatusBadRequest, "invalid_body", "Informe o nome do pagador")
		return "", "", "", nil, false
	}
	if !validCPF(taxID) {
		writeError(w, http.StatusBadRequest, "invalid_body", "CPF do pagador inválido")
		return "", "", "", nil, false
	}
	if email != "" && !validEmail(email) {
		writeError(w, http.StatusBadRequest, "invalid_body", "E-mail do pagador inválido")
		return "", "", "", nil, false
	}
	existing, err := s.store.GetCustomerByTaxID(r.Context(), taxID)
	switch {
	case err == nil:
		cid := existing.ID
		customerID = &cid
		name = existing.Name
		if existing.Email != "" {
			email = existing.Email
		}
	case errors.Is(err, pgx.ErrNoRows):
		if p.SaveCustomer {
			phone := onlyDigits(p.Phone)
			if phone != "" && !validPhone(phone) {
				writeError(w, http.StatusBadRequest, "invalid_body", "Telefone inválido")
				return "", "", "", nil, false
			}
			cust, cerr := s.store.UpsertCustomer(r.Context(), 0, taxID, phone, name, email)
			if cerr != nil {
				writeError(w, http.StatusInternalServerError, "db_error", "Falha ao salvar o cliente")
				return "", "", "", nil, false
			}
			cid := cust.ID
			customerID = &cid
		}
	default:
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao consultar o cliente")
		return "", "", "", nil, false
	}
	if p.SendEmail && !validEmail(email) {
		writeError(w, http.StatusBadRequest, "invalid_body", "Sem e-mail válido para o envio")
		return "", "", "", nil, false
	}
	return name, taxID, email, customerID, true
}

// handleCreateManualCharge gera um PIX avulso a partir do dashboard, para um cliente
// existente (customerId) OU um pagador avulso (nome + CPF). Mesmo caminho do checkout
// (createAndPersistCharge), com public_token e payer_tax_id setados — a cobrança entra
// no histórico do cliente e dispara o comprovante automático no pagamento.
func (s *Server) handleCreateManualCharge(w http.ResponseWriter, r *http.Request) {
	var in manualChargeInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	if in.AmountCents <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "amountCents deve ser > 0")
		return
	}
	method := in.Method
	if method == "" {
		method = "pix"
	}
	if method != "pix" && method != "boleto" {
		writeError(w, http.StatusBadRequest, "invalid_body", "method deve ser 'pix' ou 'boleto'")
		return
	}
	if method == "boleto" && s.efiCobr == nil {
		writeError(w, http.StatusServiceUnavailable, "boleto_unavailable", "Boleto indisponível no momento")
		return
	}

	payerName, payerTaxID, payerEmail, customerID, ok := s.resolveManualPayer(w, r, in.manualPayerInput)
	if !ok {
		return
	}

	if in.DueDate == "" {
		in.DueDate = time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	} else {
		d, err := time.Parse("2006-01-02", in.DueDate)
		if err != nil || d.Before(time.Now().Truncate(24*time.Hour)) {
			writeError(w, http.StatusBadRequest, "invalid_body", "dueDate deve ser uma data válida no formato YYYY-MM-DD, hoje ou futura")
			return
		}
	}
	desc := strings.TrimSpace(in.Description)
	if desc == "" {
		desc = "Cobrança avulsa"
	}

	c := &Charge{
		Kind: "avulso", CustomerID: customerID, AmountCents: in.AmountCents,
		DueDate: in.DueDate, Provider: "efi", Method: method, CorrelationID: newCorrelationID(),
		PublicToken: newPublicToken(), payerTaxID: payerTaxID,
	}
	st := &Student{Name: payerName, TaxID: payerTaxID, Email: payerEmail, Phone: onlyDigits(in.Phone)} // nome/CPF p/ o gateway (boleto: phone_number só dígitos)
	if err := s.createAndPersistCharge(r.Context(), c, st, desc); err != nil {
		var pe *ProviderError
		if errors.As(err, &pe) {
			slog.Warn("manual charge: erro do gateway", "status", pe.Status, "message", pe.Message)
			writeError(w, http.StatusUnprocessableEntity, "provider_error", clientSafeGatewayMsg(pe.Message))
			return
		}
		slog.Warn("manual charge: falha no provider", "err", err)
		writeError(w, http.StatusUnprocessableEntity, "provider_error", "Falha ao gerar a cobrança. Tente novamente.")
		return
	}

	// Produto do catálogo (opcional): registra o item para aparecer no histórico do cliente.
	if in.ProductID != 0 {
		if p, perr := s.store.GetProductByID(r.Context(), in.ProductID); perr == nil {
			pid := p.ID
			if e := s.store.InsertChargeItems(r.Context(), c.ID, []ChargeItem{{ProductID: &pid, Name: p.Name, PriceCents: in.AmountCents, Quantity: 1}}); e != nil {
				slog.Warn("manual charge: falha ao gravar item do produto", "charge", c.ID, "err", e)
			}
		}
	}

	// PIX: garante o QR mesmo quando a Efí não devolve a imagem (boleto não tem QR).
	if method == "pix" && c.QRCode == "" {
		c.QRCode = qrPNGDataURI(c.BRCode)
	}
	c.PayerName = payerName // p/ o modal; createAndPersistCharge não seta

	// Envia a cobrança por email ao cliente (best-effort: não falha a cobrança já criada).
	if in.SendEmail && payerEmail != "" && s.email != nil {
		payURL := s.cfg.PublicPayURL + "/pay/" + c.PublicToken
		var subject, body string
		if method == "boleto" {
			subject = "Boleto — Santos Tech"
			body = boletoEmailHTML(payerName, c.AmountCents, c.DueDate, desc, payURL, c.PDFURL)
		} else {
			subject = "Cobrança PIX — Santos Tech"
			body = chargeEmailHTML(payerName, c.AmountCents, c.DueDate, desc, payURL)
		}
		if err := s.email.send(r.Context(), payerEmail, subject, body); err != nil {
			slog.Warn("manual charge: falha ao enviar email de cobrança", "charge", c.ID, "err", err)
		}
	}

	writeJSON(w, http.StatusCreated, c)
}

type manualRecurrenceInput struct {
	manualPayerInput
	ProductID   int64  `json:"productId"` // opcional: vincula a assinatura ao produto
	AmountCents int64  `json:"amountCents"`
	Periodicity string `json:"periodicity"` // SEMANAL|MENSAL|TRIMESTRAL|SEMESTRAL|ANUAL
	DueDay      int    `json:"dueDay"`      // 1-28
	StartDate   string `json:"startDate"`   // YYYY-MM-DD; default amanhã (futura)
	EndDate     string `json:"endDate"`     // YYYY-MM-DD; opcional
	Description string `json:"description"` // objeto da assinatura
}

// handleCreateManualRecurrence (admin) cria uma assinatura PIX Automático pelo dashboard,
// para cliente existente OU avulso, com as configs escolhidas. O QR/copia-e-cola devolvido
// AUTORIZA a assinatura (não é pagamento); o débito segue os ciclos após a autorização.
func (s *Server) handleCreateManualRecurrence(w http.ResponseWriter, r *http.Request) {
	var in manualRecurrenceInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON inválido")
		return
	}
	if in.AmountCents <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "amountCents deve ser > 0")
		return
	}
	if !validPeriodicities[in.Periodicity] {
		writeError(w, http.StatusBadRequest, "invalid_body", "periodicity inválida")
		return
	}
	// A Efí limita o dia de débito a 1–28 (evita meses sem 29/30/31).
	if in.DueDay < 1 || in.DueDay > 28 {
		writeError(w, http.StatusBadRequest, "invalid_body", "dueDay deve estar entre 1 e 28")
		return
	}
	start := strings.TrimSpace(in.StartDate)
	todayStr := time.Now().Format("2006-01-02")
	if start == "" {
		// dataInicial deve ser FUTURA (a Efí recusa data = criação); default = amanhã.
		start = time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	} else {
		if _, err := time.Parse("2006-01-02", start); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_body", "startDate deve ser YYYY-MM-DD")
			return
		}
		if start <= todayStr { // comparação lexicográfica de YYYY-MM-DD = cronológica
			writeError(w, http.StatusBadRequest, "invalid_body", "startDate deve ser uma data futura")
			return
		}
	}
	end := strings.TrimSpace(in.EndDate)
	if end != "" {
		if _, err := time.Parse("2006-01-02", end); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_body", "endDate deve ser YYYY-MM-DD")
			return
		}
	}

	payerName, payerTaxID, payerEmail, customerID, ok := s.resolveManualPayer(w, r, in.manualPayerInput)
	if !ok {
		return
	}

	object := strings.TrimSpace(in.Description)
	if object == "" {
		object = "Assinatura Santos Tech"
	}
	token := newPublicToken()
	dueDay := in.DueDay
	rec := &Recurrence{
		CustomerID:  customerID,
		PayerTaxID:  payerTaxID,
		PayerName:   payerName,
		AmountCents: in.AmountCents,
		Periodicity: in.Periodicity,
		DueDay:      &dueDay,
		StartDate:   start,
		EndDate:     end,
		Journey:     2,
		PublicToken: token,
	}
	// Produto do catálogo (opcional): vincula para habilitar a 1ª cobrança na aprovação.
	if in.ProductID != 0 {
		if _, perr := s.store.GetProductByID(r.Context(), in.ProductID); perr != nil {
			writeError(w, http.StatusNotFound, "not_found", "Produto não encontrado")
			return
		}
		pid := in.ProductID
		rec.ProductID = &pid
	}
	// Persiste primeiro (status 'pending_auth') para ter o ID p/ o contrato.
	if err := s.recs.CreateRecurrence(r.Context(), rec); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao criar a assinatura")
		return
	}

	res, err := s.efi.CreateRecurrence(r.Context(), RecurrenceRequest{
		Contract:    "Assinatura #" + strconv.FormatInt(rec.ID, 10),
		Object:      object,
		PayerName:   payerName,
		PayerTaxID:  payerTaxID,
		AmountCents: in.AmountCents,
		Periodicity: in.Periodicity,
		StartDate:   start,
		EndDate:     end,
	})
	if err != nil {
		_ = s.recs.SetRecurrenceStatus(r.Context(), rec.ID, "rejected")
		var pe *ProviderError
		if errors.As(err, &pe) {
			slog.Warn("manual recurrence: erro do gateway", "status", pe.Status, "message", pe.Message)
			writeError(w, http.StatusUnprocessableEntity, "provider_error", clientSafeGatewayMsg(pe.Message))
			return
		}
		slog.Warn("manual recurrence: falha no provider", "err", err)
		writeError(w, http.StatusUnprocessableEntity, "provider_error", "Falha ao criar a assinatura. Tente novamente.")
		return
	}
	if err := s.recs.UpdateRecurrenceAuth(r.Context(), rec.ID, res.EfiIDRec, res.BRCode, res.QRCode, res.Status); err != nil {
		slog.Warn("manual recurrence: falha ao gravar autorização", "rec", rec.ID, "err", err)
	}
	rec.EfiIDRec, rec.BRCode, rec.QRCode, rec.Status = res.EfiIDRec, res.BRCode, res.QRCode, res.Status
	if rec.QRCode == "" {
		rec.QRCode = qrPNGDataURI(rec.BRCode)
	}

	// E-mail de autorização (best-effort): link para a tela onde o cliente autoriza.
	if in.SendEmail && payerEmail != "" && s.email != nil {
		authURL := s.cfg.PublicPayURL + "/subscribe/" + token
		body := subscriptionEmailHTML(payerName, in.AmountCents, in.Periodicity, dueDay, authURL)
		if e := s.email.send(r.Context(), payerEmail, "Autorize sua assinatura — Santos Tech", body); e != nil {
			slog.Warn("manual recurrence: falha ao enviar email de autorização", "rec", rec.ID, "err", e)
		}
	}

	writeJSON(w, http.StatusCreated, rec)
}

// createAndPersistCharge cria no provider, grava e dispara email. Reusado pela recorrência.
// Boleto (c.Method=="boleto") vai pela API Cobranças; o resto segue o fluxo PIX.
func (s *Server) createAndPersistCharge(ctx context.Context, c *Charge, st *Student, desc string) error {
	if c.Method == "boleto" {
		return s.createAndPersistBoleto(ctx, c, st, desc)
	}
	expires := time.Now().AddDate(0, 0, 3).Format(time.RFC3339)
	if d, err := time.Parse("2006-01-02", c.DueDate); err == nil {
		expires = d.Add(23 * time.Hour).Format(time.RFC3339)
	}
	// RESERVA a linha ANTES de chamar a Efí. A ordem inversa (criar na Efí e só então
	// inserir) deixava um QR PAGÁVEL sem cobrança nenhuma no banco quando o INSERT
	// falhava: o cliente pagava e o webhook não tinha o que casar.
	if err := s.reserveCharge(ctx, c); err != nil {
		return err
	}
	res, err := s.provider.CreateCharge(ctx, ChargeRequest{
		CorrelationID: c.CorrelationID, AmountCents: c.AmountCents,
		PayerName: st.Name, PayerTaxID: st.TaxID, Description: desc, ExpiresAt: expires,
	})
	if err != nil {
		s.abandonCharge(ctx, c)
		return err
	}
	c.ProviderChargeID, c.BRCode, c.QRCode = res.ProviderChargeID, res.BRCode, res.QRCode
	// O gateway pode devolver o correlationID efetivo (txid) que volta no webhook —
	// gravamos esse valor para a reconciliação do CHARGE_PAID casar.
	if res.CorrelationID != "" {
		c.CorrelationID = res.CorrelationID
	}
	return s.activateCharge(ctx, c)
}

// chargeWriteStoreOf devolve s.chargeWr se injetado (testes), senão s.store.
func (s *Server) chargeWriteStoreOf() chargeWriteStore {
	if s.chargeWr != nil {
		return s.chargeWr
	}
	return s.store
}

// reserveCharge insere a cobranca em 'creating' (linha reservada, ainda sem gateway).
func (s *Server) reserveCharge(ctx context.Context, c *Charge) error {
	c.Status = "creating"
	if err := s.chargeWriteStoreOf().InsertCharge(ctx, c); err != nil {
		c.Status = ""
		return err
	}
	return nil
}

// activateCharge promove a reserva a 'pending' com os dados devolvidos pelo gateway.
func (s *Server) activateCharge(ctx context.Context, c *Charge) error {
	ok, err := s.chargeWriteStoreOf().ActivateCharge(ctx, c.ID, c)
	if err != nil {
		// A cobranca EXISTE no gateway e e pagavel; a linha ficou em 'creating'.
		// Precisa aparecer: e dinheiro que pode entrar sem baixa automatica.
		slog.Error("cobranca criada no gateway mas nao ativada no banco",
			"charge", c.ID, "correlation", c.CorrelationID, "err", err)
		return err
	}
	if !ok {
		slog.Error("cobranca criada no gateway mas a reserva nao estava em 'creating'",
			"charge", c.ID, "correlation", c.CorrelationID)
	}
	c.Status = "pending"
	return nil
}

// abandonCharge cancela a reserva quando o gateway recusa a cobranca.
func (s *Server) abandonCharge(ctx context.Context, c *Charge) {
	if c.ID == 0 {
		return
	}
	if err := s.chargeWriteStoreOf().AbandonCharge(context.WithoutCancel(ctx), c.ID); err != nil {
		slog.Warn("falha ao cancelar a reserva da cobranca", "charge", c.ID, "err", err)
	}
}

// createAndPersistBoleto emite o boleto na API Cobranças e grava a cobrança. A confirmação
// de pagamento chega pela notificação (POST /webhooks/efi/cobr), casada por provider_charge_id.
func (s *Server) createAndPersistBoleto(ctx context.Context, c *Charge, st *Student, desc string) error {
	notifyURL := ""
	if s.cfg.EFIWebhookURL != "" {
		notifyURL = s.cfg.EFIWebhookURL + "/cobr"
		if s.cfg.EFIWebhookSecret != "" {
			notifyURL += "?token=" + s.cfg.EFIWebhookSecret
		}
	}
	// Mesma reserva do PIX: a linha nasce em 'creating' antes de existir na Efi.
	if err := s.reserveCharge(ctx, c); err != nil {
		return err
	}
	res, err := s.efiCobr.CreateBoleto(ctx, BoletoRequest{
		AmountCents: c.AmountCents, PayerName: st.Name, PayerTaxID: st.TaxID,
		PayerEmail: st.Email, PayerPhone: st.Phone, Description: desc,
		DueDate: c.DueDate, CustomID: c.CorrelationID, NotificationURL: notifyURL,
	})
	if err != nil {
		s.abandonCharge(ctx, c)
		return err
	}
	c.ProviderChargeID = res.ChargeID // charge_id do Efí → casa a notificação
	c.BRCode = res.Line               // linha digitável (copia-e-cola do boleto)
	c.PDFURL = res.PDFURL
	c.Barcode = res.Line
	return s.activateCharge(ctx, c)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao calcular estatísticas")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleListCharges(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	studentID, _ := strconv.ParseInt(r.URL.Query().Get("student_id"), 10, 64)
	list, err := s.store.ListCharges(r.Context(), status, studentID, pageFromRequest(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetCharge(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	c, err := s.store.GetCharge(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Cobrança não encontrada")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleStudentCharges(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	list, err := s.store.ListCharges(r.Context(), "", id, pageFromRequest(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}
