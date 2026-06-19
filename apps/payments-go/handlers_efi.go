package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// efiOps isola as operações Efí expostas via dashboard (o *efiProvider em prod, fake nos testes).
type efiOps interface {
	GetBalance(ctx context.Context) (int64, error)
	// GetReceipt baixa o comprovante de um Pix pelo txid (= correlationID da cobrança).
	// Retorna o content-type e os bytes do comprovante.
	GetReceipt(ctx context.Context, txid string) (contentType string, body []byte, err error)
	// ListMED lista infrações MED (disputas Pix) na janela [inicio, fim].
	ListMED(ctx context.Context, inicio, fim time.Time) ([]MEDInfraction, error)
	// RequestReport solicita geração do extrato de conciliação Pix para a data dada
	// (formato YYYY-MM-DD). Retorna o ID do relatório (resposta 202 da Efí).
	RequestReport(ctx context.Context, dataMovimento string) (reportID string, err error)
	// GetReport consulta o status do relatório. Retorna ("done", contentType, csv, nil)
	// quando pronto (200) ou ("processing", "", nil, nil) quando ainda processando (202).
	GetReport(ctx context.Context, id string) (status string, contentType string, body []byte, err error)
}

// chargeReader isola o acesso a cobranças no banco (o *Store em prod, fake nos testes).
type chargeReader interface {
	GetCharge(ctx context.Context, id int64) (*Charge, error)
}

func (s *Server) handleEfiMED(w http.ResponseWriter, r *http.Request) {
	rng := parseRange(r.URL.Query().Get("range"))
	items, err := s.efi.ListMED(r.Context(), rng.From, rng.To)
	if err != nil {
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao consultar infrações MED na Efí")
		return
	}
	if items == nil {
		items = []MEDInfraction{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleEfiBalance(w http.ResponseWriter, r *http.Request) {
	cents, err := s.efi.GetBalance(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao consultar saldo na Efí")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"availableCents": cents})
}

func (s *Server) handleReceipt(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	c, err := s.charges.GetCharge(r.Context(), id)
	if err != nil || c == nil {
		writeError(w, http.StatusNotFound, "not_found", "Cobrança não encontrada")
		return
	}
	if c.Status != "paid" {
		writeError(w, http.StatusConflict, "not_paid", "Cobrança ainda não foi paga")
		return
	}
	ct, body, err := s.efi.GetReceipt(r.Context(), c.CorrelationID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao obter comprovante na Efí")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="comprovante-`+r.PathValue("id")+`.pdf"`)
	w.Write(body) //nolint:errcheck
}

// handleReportRequest solicita a geração de um relatório de extrato de conciliação.
// POST /efi/reports   body: {"date":"YYYY-MM-DD"}   → {"reportId":"..."}
func (s *Server) handleReportRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Date string `json:"date"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Date == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Campo date obrigatório (YYYY-MM-DD)")
		return
	}
	if _, err := time.Parse("2006-01-02", req.Date); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Campo date deve ser YYYY-MM-DD")
		return
	}
	id, err := s.efi.RequestReport(r.Context(), req.Date)
	if err != nil {
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao solicitar relatório na Efí")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"reportId": id})
}

// handleReportGet consulta o status/conteúdo de um relatório de conciliação.
// GET /efi/reports/{id}
//   - processando → 200 {"status":"processing"}
//   - pronto      → 200 text/csv com Content-Disposition
func (s *Server) handleReportGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "ID do relatório obrigatório")
		return
	}
	status, ct, body, err := s.efi.GetReport(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao consultar relatório na Efí")
		return
	}
	if status == "processing" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "processing"})
		return
	}
	// status == "done": streama o CSV
	if ct == "" {
		ct = "text/csv"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="extrato-conciliacao-`+id+`.csv"`)
	w.Write(body) //nolint:errcheck
}
