package main

import (
	"context"
	"net/http"
)

// efiOps isola as operações Efí expostas via dashboard (o *efiProvider em prod, fake nos testes).
type efiOps interface {
	GetBalance(ctx context.Context) (int64, error)
}

func (s *Server) handleEfiBalance(w http.ResponseWriter, r *http.Request) {
	cents, err := s.efi.GetBalance(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "efi_error", "Falha ao consultar saldo na Efí")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"availableCents": cents})
}
