package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// parseStatementRange é um superconjunto de parseRange para o extrato.
// Aceita todos os tokens do contrato da UI:
//   - today, month, 7d, 30d, 90d, 12m, all (repassados ao parseRange)
//   - AAAA-MM-DD:AAAA-MM-DD (intervalo explícito)
//
// Tokens não reconhecidos caem para o default de parseRange (30d).
// NÃO altera o comportamento de parseRange (usado em analytics).
func parseStatementRange(s string) AnalyticsRange {
	// Tenta intervalo explícito "AAAA-MM-DD:AAAA-MM-DD".
	if idx := strings.IndexByte(s, ':'); idx > 0 && idx < len(s)-1 {
		fromStr := s[:idx]
		toStr := s[idx+1:]
		from, errF := time.Parse("2006-01-02", fromStr)
		to, errT := time.Parse("2006-01-02", toStr)
		if errF == nil && errT == nil && !from.After(to) {
			// "to" é um dia inclusivo: avança um dia para que o intervalo seja [from, to+1d).
			toEnd := to.AddDate(0, 0, 1)
			r := AnalyticsRange{
				Key:    s,
				Bucket: "day",
				From:   from,
				To:     toEnd,
			}
			window := toEnd.Sub(from)
			r.PrevTo = from
			r.PrevFrom = from.Add(-window)
			return r
		}
	}
	// today: meia-noite local até agora.
	if s == "today" {
		now := time.Now()
		from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		r := AnalyticsRange{Key: "today", Bucket: "day", From: from, To: now}
		window := now.Sub(from)
		r.PrevTo = from
		r.PrevFrom = from.Add(-window)
		return r
	}
	// month: 1º dia do mês corrente até agora.
	if s == "month" {
		now := time.Now()
		from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		r := AnalyticsRange{Key: "month", Bucket: "day", From: from, To: now}
		window := now.Sub(from)
		r.PrevTo = from
		r.PrevFrom = from.Add(-window)
		return r
	}
	// Demais tokens (7d, 30d, 90d, 12m, all e fallback).
	return parseRange(s)
}

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

	rng := parseStatementRange(r.URL.Query().Get("range"))
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
