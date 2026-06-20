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

type manualChargeInput struct {
	CustomerID   int64  `json:"customerId"` // opcional: cliente existente
	PayerName    string `json:"payerName"`  // avulso
	PayerTaxID   string `json:"payerTaxId"` // avulso (CPF)
	PayerEmail   string `json:"payerEmail"` // avulso (destino do email de cobrança)
	Phone        string `json:"phone"`      // avulso (ao salvar como cliente)
	AmountCents  int64  `json:"amountCents"`
	Description  string `json:"description"`
	DueDate      string `json:"dueDate"`      // YYYY-MM-DD; default hoje+3
	SendEmail    bool   `json:"sendEmail"`    // envia o PIX por email ao cliente
	SaveCustomer bool   `json:"saveCustomer"` // avulso: cria/atualiza o cliente (consolida por CPF)
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

	// Resolve o pagador: cliente existente OU avulso (nome + CPF).
	var payerName, payerTaxID, payerEmail string
	var customerID *int64
	if in.CustomerID != 0 {
		cust, err := s.store.GetCustomerDetail(r.Context(), in.CustomerID)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "Cliente não encontrado")
			return
		}
		cid := cust.ID
		customerID = &cid
		payerName, payerTaxID, payerEmail = cust.Name, cust.TaxID, cust.Email
	} else {
		payerName = strings.TrimSpace(in.PayerName)
		payerTaxID = onlyDigits(in.PayerTaxID)
		payerEmail = strings.TrimSpace(in.PayerEmail)
		if !validName(payerName) {
			writeError(w, http.StatusBadRequest, "invalid_body", "Informe o nome do pagador")
			return
		}
		if !validCPF(payerTaxID) {
			writeError(w, http.StatusBadRequest, "invalid_body", "CPF do pagador inválido")
			return
		}
		if payerEmail != "" && !validEmail(payerEmail) {
			writeError(w, http.StatusBadRequest, "invalid_body", "E-mail do pagador inválido")
			return
		}
		// Se o CPF já é de um cliente cadastrado, trata como AQUELE cliente: liga a cobrança
		// a ele e usa os dados canônicos (nome/e-mail) — NÃO sobrescreve com o que foi digitado.
		existing, err := s.store.GetCustomerByTaxID(r.Context(), payerTaxID)
		switch {
		case err == nil:
			customerID = &existing.ID
			payerName = existing.Name
			if existing.Email != "" {
				payerEmail = existing.Email
			}
		case errors.Is(err, pgx.ErrNoRows):
			// CPF novo: cria o cliente só se o admin pediu (salvar como cliente). user_id=0 =
			// sem conta de usuário; consolida por CPF e habilita o comprovante no pagamento.
			if in.SaveCustomer {
				phone := onlyDigits(in.Phone)
				if phone != "" && !validPhone(phone) {
					writeError(w, http.StatusBadRequest, "invalid_body", "Telefone inválido")
					return
				}
				cust, cerr := s.store.UpsertCustomer(r.Context(), 0, payerTaxID, phone, payerName, payerEmail)
				if cerr != nil {
					writeError(w, http.StatusInternalServerError, "db_error", "Falha ao salvar o cliente")
					return
				}
				customerID = &cust.ID
			}
		default:
			writeError(w, http.StatusInternalServerError, "db_error", "Falha ao consultar o cliente")
			return
		}
		// Valida o envio de e-mail sobre o e-mail FINAL (resolvido do cliente, quando existir).
		if in.SendEmail && !validEmail(payerEmail) {
			writeError(w, http.StatusBadRequest, "invalid_body", "Sem e-mail válido para enviar a cobrança")
			return
		}
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
		DueDate: in.DueDate, Provider: "efi", CorrelationID: newCorrelationID(),
		PublicToken: newPublicToken(), payerTaxID: payerTaxID,
	}
	st := &Student{Name: payerName, TaxID: payerTaxID, Email: payerEmail} // nome/CPF p/ o gateway
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

	// Garante o QR mesmo quando a Efí não devolve a imagem (best-effort → sempre presente).
	if c.QRCode == "" {
		c.QRCode = qrPNGDataURI(c.BRCode)
	}
	c.PayerName = payerName // p/ o modal; createAndPersistCharge não seta

	// Envia o PIX por email ao cliente (best-effort: não falha a cobrança já criada).
	if in.SendEmail && payerEmail != "" && s.email != nil {
		payURL := s.cfg.PublicPayURL + "/pay/" + c.PublicToken
		body := chargeEmailHTML(payerName, c.AmountCents, c.DueDate, desc, payURL)
		if err := s.email.send(r.Context(), payerEmail, "Cobrança PIX — Santos Tech", body); err != nil {
			slog.Warn("manual charge: falha ao enviar email de cobrança", "charge", c.ID, "err", err)
		}
	}

	writeJSON(w, http.StatusCreated, c)
}

// createAndPersistCharge cria no provider, grava e dispara email. Reusado pela recorrência.
func (s *Server) createAndPersistCharge(ctx context.Context, c *Charge, st *Student, desc string) error {
	expires := time.Now().AddDate(0, 0, 3).Format(time.RFC3339)
	if d, err := time.Parse("2006-01-02", c.DueDate); err == nil {
		expires = d.Add(23 * time.Hour).Format(time.RFC3339)
	}
	res, err := s.provider.CreateCharge(ctx, ChargeRequest{
		CorrelationID: c.CorrelationID, AmountCents: c.AmountCents,
		PayerName: st.Name, PayerTaxID: st.TaxID, Description: desc, ExpiresAt: expires,
	})
	if err != nil {
		return err
	}
	c.ProviderChargeID, c.BRCode, c.QRCode = res.ProviderChargeID, res.BRCode, res.QRCode
	// O gateway pode devolver o correlationID efetivo (txid) que volta no webhook —
	// gravamos esse valor para a reconciliação do CHARGE_PAID casar.
	if res.CorrelationID != "" {
		c.CorrelationID = res.CorrelationID
	}
	if err := s.store.InsertCharge(ctx, c); err != nil {
		return err
	}
	return nil
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
	list, err := s.store.ListCharges(r.Context(), status, studentID)
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
	list, err := s.store.ListCharges(r.Context(), "", id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, list)
}
