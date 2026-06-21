package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// statementStore isola as operações de extrato (o *Store em prod, fake nos testes).
type statementStore interface {
	ListMovements(ctx context.Context, from, to time.Time) ([]Movement, error)
}

// statementStoreOf devolve o store de extrato efetivo: usa s.statement quando injetado
// (testes), e cai para s.store em produção.
func (s *Server) statementStoreOf() statementStore {
	if s.statement != nil {
		return s.statement
	}
	if s.store != nil {
		return s.store
	}
	return nil
}

// handleStatement responde GET /statement?range=<key> → []Movement ordenado por data desc.
// Reutiliza parseRange (analytics.go) para as janelas de tempo.
// Requer admin (registrado em server.go via requireAdmin).
// O CSV de conciliação Efí (GET /efi/reports/{id}) não é alterado — são dois extratos
// independentes: este é local (cobranças pagas + saques), o da Efí é de liquidação Pix.
func (s *Server) handleStatement(w http.ResponseWriter, r *http.Request) {
	st := s.statementStoreOf()
	if st == nil {
		writeError(w, http.StatusServiceUnavailable, "db_unavailable", "Banco de dados não disponível")
		return
	}

	rng := parseRange(r.URL.Query().Get("range"))
	mvs, err := st.ListMovements(r.Context(), rng.From, rng.To)
	if err != nil {
		slog.Warn("statement: falha ao listar movimentos", "err", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Falha ao consultar extrato")
		return
	}

	if mvs == nil {
		mvs = []Movement{}
	}
	writeJSON(w, http.StatusOK, mvs)
}
