package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

func newCorrelationID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "stpay_" + hex.EncodeToString(b)
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
	}
	c := &Charge{
		Kind: in.Kind, StudentID: st.ID, AmountCents: in.AmountCents,
		DueDate: in.DueDate, Provider: "dotfy", CorrelationID: newCorrelationID(),
	}
	if err := s.createAndPersistCharge(r.Context(), c, st, in.Description); err != nil {
		writeError(w, http.StatusBadGateway, "provider_error", "Falha ao gerar cobrança no gateway")
		return
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
	if err := s.store.InsertCharge(ctx, c); err != nil {
		return err
	}
	// email é best-effort: não falha a cobrança (que já está válida).
	if mailErr := s.email.send(ctx, st.Email, "Sua cobrança Pix — Santos Tech", pixEmailHTML(st.Name, c.AmountCents, c.BRCode)); mailErr != nil {
		slog.Warn("falha ao enviar email da cobrança", "charge", c.ID, "err", mailErr)
	}
	return nil
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
