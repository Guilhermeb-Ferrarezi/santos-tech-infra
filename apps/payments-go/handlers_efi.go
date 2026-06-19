package main

import (
	"context"
	"net/http"
	"strconv"
)

// efiOps isola as operações Efí expostas via dashboard (o *efiProvider em prod, fake nos testes).
type efiOps interface {
	GetBalance(ctx context.Context) (int64, error)
	// GetReceipt baixa o comprovante de um Pix pelo txid (= correlationID da cobrança).
	// Retorna o content-type e os bytes do comprovante.
	GetReceipt(ctx context.Context, txid string) (contentType string, body []byte, err error)
}

// chargeReader isola o acesso a cobranças no banco (o *Store em prod, fake nos testes).
type chargeReader interface {
	GetCharge(ctx context.Context, id int64) (*Charge, error)
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
