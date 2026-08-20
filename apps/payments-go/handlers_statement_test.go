package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ── Fake store de extrato ─────────────────────────────────────────────────────

// fakeStatementStore implementa statementStore sem banco.
type fakeStatementStore struct {
	movements []Movement
	err       error
}

func (f *fakeStatementStore) ListMovements(_ context.Context, from, to time.Time, _ listPage) ([]Movement, error) {
	if f.err != nil {
		return nil, f.err
	}
	// Filtra pelo intervalo para simular o comportamento do banco.
	var out []Movement
	for _, m := range f.movements {
		if !m.Date.Before(from) && m.Date.Before(to) {
			out = append(out, m)
		}
	}
	sortMovements(out)
	return out, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// buildStatementServer monta um Server mínimo com o fakeStatementStore injetado.
func buildStatementServer(st statementStore) *Server {
	return &Server{statement: st}
}

// baseTime é uma âncora fixa para os testes de ordenação.
var baseTime = time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

// ── Testes ────────────────────────────────────────────────────────────────────

// TestHandleStatement_Vazio: sem movimentos → 200 array vazio.
func TestHandleStatement_Vazio(t *testing.T) {
	s := buildStatementServer(&fakeStatementStore{})
	req := httptest.NewRequest(http.MethodGet, "/statement?range=30d", nil)
	w := httptest.NewRecorder()
	s.handleStatement(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var out []Movement
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("esperado 0 movimentos, veio %d", len(out))
	}
}

// TestHandleStatement_MovimentosEntradaESaida: range retorna movimentos de entrada
// (pagamento pix) e saída (saque), ordenados por data decrescente.
func TestHandleStatement_MovimentosEntradaESaida(t *testing.T) {
	now := time.Now().UTC()
	older := now.Add(-2 * 24 * time.Hour) // 2 dias atrás (entrada mais antiga)
	newer := now.Add(-1 * 24 * time.Hour) // 1 dia atrás (saída mais recente)

	st := &fakeStatementStore{
		movements: []Movement{
			{
				ID:           1,
				Type:         "pix_in",
				Direction:    "in",
				Method:       "pix",
				ValueCents:   10000,
				CustomerName: "João Silva",
				Date:         older,
			},
			{
				ID:         2,
				Type:       "payout",
				Direction:  "out",
				Method:     "",
				ValueCents: 5000,
				Date:       newer,
			},
		},
	}

	s := buildStatementServer(st)
	req := httptest.NewRequest(http.MethodGet, "/statement?range=30d", nil)
	w := httptest.NewRecorder()
	s.handleStatement(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var out []Movement
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("esperado 2 movimentos, veio %d", len(out))
	}

	// O mais recente (saque, newer) deve vir primeiro.
	if out[0].Direction != "out" {
		t.Fatalf("esperado primeiro movimento direction=out (mais recente), veio %q", out[0].Direction)
	}
	if out[0].Type != "payout" {
		t.Fatalf("esperado type=payout no primeiro, veio %q", out[0].Type)
	}
	if out[0].ValueCents != 5000 {
		t.Fatalf("esperado valueCents=5000 no payout, veio %d", out[0].ValueCents)
	}

	// O mais antigo (PIX, older) deve vir depois.
	if out[1].Direction != "in" {
		t.Fatalf("esperado segundo movimento direction=in, veio %q", out[1].Direction)
	}
	if out[1].Type != "pix_in" {
		t.Fatalf("esperado type=pix_in no segundo, veio %q", out[1].Type)
	}
	if out[1].ValueCents != 10000 {
		t.Fatalf("esperado valueCents=10000 no pix_in, veio %d", out[1].ValueCents)
	}
	if out[1].CustomerName != "João Silva" {
		t.Fatalf("customerName esperado 'João Silva', veio %q", out[1].CustomerName)
	}
}

// TestHandleStatement_OrdenacaoDecrescente: garante que movimentos são devolvidos em
// ordem decrescente por data (mais novo primeiro), independente da ordem de inserção.
func TestHandleStatement_OrdenacaoDecrescente(t *testing.T) {
	now := time.Now().UTC()
	t1 := now.Add(-72 * time.Hour) // mais antigo (dentro de 7d)
	t2 := now.Add(-48 * time.Hour)
	t3 := now.Add(-24 * time.Hour) // mais recente (dentro de 7d)

	// Inseridos fora de ordem propositalmente.
	st := &fakeStatementStore{
		movements: []Movement{
			{ID: 2, Type: "boleto_in", Direction: "in", Method: "boleto", ValueCents: 2000, Date: t2},
			{ID: 3, Type: "payout", Direction: "out", Method: "", ValueCents: 3000, Date: t3},
			{ID: 1, Type: "pix_in", Direction: "in", Method: "pix", ValueCents: 1000, Date: t1},
		},
	}

	s := buildStatementServer(st)
	req := httptest.NewRequest(http.MethodGet, "/statement?range=7d", nil)
	w := httptest.NewRecorder()
	s.handleStatement(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var out []Movement
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("esperado 3 movimentos, veio %d", len(out))
	}
	if out[0].ID != 3 || out[1].ID != 2 || out[2].ID != 1 {
		t.Fatalf("ordem errada: IDs esperados [3 2 1], vieram [%d %d %d]",
			out[0].ID, out[1].ID, out[2].ID)
	}
}

// TestHandleStatement_DBError: erro no store → 500 db_error.
func TestHandleStatement_DBError(t *testing.T) {
	st := &fakeStatementStore{err: errFake}
	s := buildStatementServer(st)
	req := httptest.NewRequest(http.MethodGet, "/statement", nil)
	w := httptest.NewRecorder()
	s.handleStatement(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("esperado 500, veio %d: %s", w.Code, w.Body.String())
	}
	assertCode(t, w.Body.Bytes(), "db_error")
}

// TestHandleStatement_StoreNil: sem store configurado → 503 db_unavailable.
func TestHandleStatement_StoreNil(t *testing.T) {
	s := &Server{} // statement=nil, store=nil
	req := httptest.NewRequest(http.MethodGet, "/statement", nil)
	w := httptest.NewRecorder()
	s.handleStatement(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("esperado 503, veio %d: %s", w.Code, w.Body.String())
	}
	assertCode(t, w.Body.Bytes(), "db_unavailable")
}

// TestHandleStatement_TiposDirecionCampos: valida que os campos do JSON batem com
// a spec (direction, type, method, valueCents, date presentes).
func TestHandleStatement_TiposDirecionCampos(t *testing.T) {
	now := time.Now().UTC()
	st := &fakeStatementStore{
		movements: []Movement{
			{
				ID:            10,
				Type:          "card_in",
				Direction:     "in",
				Method:        "card",
				ValueCents:    7500,
				CustomerName:  "Maria",
				CustomerEmail: "maria@example.com",
				Date:          now.Add(-1 * time.Hour),
			},
		},
	}

	s := buildStatementServer(st)
	req := httptest.NewRequest(http.MethodGet, "/statement?range=7d", nil)
	w := httptest.NewRecorder()
	s.handleStatement(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}

	var out []struct {
		ID            int64  `json:"id"`
		Type          string `json:"type"`
		Direction     string `json:"direction"`
		Method        string `json:"method"`
		ValueCents    int64  `json:"valueCents"`
		CustomerName  string `json:"customerName"`
		CustomerEmail string `json:"customerEmail"`
		Date          string `json:"date"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("esperado 1 movimento, veio %d", len(out))
	}
	m := out[0]
	if m.ID != 10 {
		t.Fatalf("id esperado 10, veio %d", m.ID)
	}
	if m.Type != "card_in" {
		t.Fatalf("type esperado card_in, veio %q", m.Type)
	}
	if m.Direction != "in" {
		t.Fatalf("direction esperado in, veio %q", m.Direction)
	}
	if m.Method != "card" {
		t.Fatalf("method esperado card, veio %q", m.Method)
	}
	if m.ValueCents != 7500 {
		t.Fatalf("valueCents esperado 7500, veio %d", m.ValueCents)
	}
	if m.CustomerName != "Maria" {
		t.Fatalf("customerName esperado 'Maria', veio %q", m.CustomerName)
	}
	if m.CustomerEmail != "maria@example.com" {
		t.Fatalf("customerEmail esperado 'maria@example.com', veio %q", m.CustomerEmail)
	}
	if m.Date == "" {
		t.Fatal("date não deve ser vazio")
	}
}

// TestSortMovements: unitário de sortMovements — garante que a ordenação é descrescente.
func TestSortMovements(t *testing.T) {
	t1 := baseTime
	t2 := baseTime.Add(time.Hour)
	t3 := baseTime.Add(2 * time.Hour)

	mvs := []Movement{
		{ID: 1, Date: t1},
		{ID: 3, Date: t3},
		{ID: 2, Date: t2},
	}
	sortMovements(mvs)

	if mvs[0].ID != 3 || mvs[1].ID != 2 || mvs[2].ID != 1 {
		t.Fatalf("sortMovements: ordem errada: IDs [%d %d %d], esperado [3 2 1]",
			mvs[0].ID, mvs[1].ID, mvs[2].ID)
	}
}

// ── Testes de parseStatementRange ─────────────────────────────────────────────

// [Fix 12] parseStatementRange aceita todos os tokens do contrato da UI.
func TestParseStatementRange_AllTokens(t *testing.T) {
	now := time.Now()
	cases := []struct {
		token     string
		expectKey string
		checkFrom func(from time.Time) bool
	}{
		{"7d", "7d", func(from time.Time) bool { return now.Sub(from) >= 7*24*time.Hour-time.Minute }},
		{"30d", "30d", func(from time.Time) bool { return now.Sub(from) >= 30*24*time.Hour-time.Minute }},
		{"90d", "90d", func(from time.Time) bool { return now.Sub(from) >= 90*24*time.Hour-time.Minute }},
		{"12m", "12m", func(from time.Time) bool { return now.Sub(from) >= 365*24*time.Hour-time.Minute }},
		{"all", "all", func(from time.Time) bool { return from.Year() <= 2020 }},
		{"today", "today", func(from time.Time) bool {
			midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			return from.Equal(midnight) || from.After(midnight.Add(-time.Second))
		}},
		{"month", "month", func(from time.Time) bool {
			firstDay := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
			return from.Equal(firstDay) || from.After(firstDay.Add(-time.Second))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			r := parseStatementRange(tc.token)
			if r.Key != tc.expectKey {
				t.Fatalf("[Fix12] token=%q: key esperado %q, veio %q", tc.token, tc.expectKey, r.Key)
			}
			if r.From.IsZero() {
				t.Fatalf("[Fix12] token=%q: From não deve ser zero", tc.token)
			}
			if r.To.IsZero() {
				t.Fatalf("[Fix12] token=%q: To não deve ser zero", tc.token)
			}
			if !tc.checkFrom(r.From) {
				t.Fatalf("[Fix12] token=%q: From=%v não satisfaz a condição esperada", tc.token, r.From)
			}
		})
	}
}

// [Fix 12] parseStatementRange aceita intervalo explícito AAAA-MM-DD:AAAA-MM-DD.
func TestParseStatementRange_ExplicitInterval(t *testing.T) {
	r := parseStatementRange("2024-01-01:2024-03-31")
	if r.Key != "2024-01-01:2024-03-31" {
		t.Fatalf("[Fix12] key esperado '2024-01-01:2024-03-31', veio %q", r.Key)
	}
	expectedFrom := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if !r.From.Equal(expectedFrom) {
		t.Fatalf("[Fix12] From esperado %v, veio %v", expectedFrom, r.From)
	}
	// To deve ser 2024-04-01 (dia seguinte ao 31/03, inclusivo).
	expectedTo := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	if !r.To.Equal(expectedTo) {
		t.Fatalf("[Fix12] To esperado %v, veio %v", expectedTo, r.To)
	}
}

// [Fix 12] parseStatementRange com intervalo inválido cai para o default (30d).
func TestParseStatementRange_InvalidInterval(t *testing.T) {
	r := parseStatementRange("invalido:data")
	// Deve ser o default (30d).
	if r.Key != "30d" {
		t.Fatalf("[Fix12] intervalo inválido deveria virar 30d, veio %q", r.Key)
	}
}

// [Fix 12] parseStatementRange: "today" abrange desde meia-noite até agora.
func TestParseStatementRange_Today(t *testing.T) {
	r := parseStatementRange("today")
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if r.From.Before(midnight.Add(-time.Second)) {
		t.Fatalf("[Fix12] today: From=%v deveria ser ≥ meia-noite (%v)", r.From, midnight)
	}
	if r.To.Before(midnight) {
		t.Fatalf("[Fix12] today: To=%v deveria ser após meia-noite", r.To)
	}
}

// [Fix 12] handleStatement aceita token "today" e retorna 200.
func TestHandleStatement_TokenToday(t *testing.T) {
	now := time.Now().UTC()
	st := &fakeStatementStore{
		movements: []Movement{
			{ID: 1, Type: "pix_in", Direction: "in", Method: "pix", ValueCents: 500, Date: now.Add(-1 * time.Hour)},
		},
	}
	s := buildStatementServer(st)
	req := httptest.NewRequest(http.MethodGet, "/statement?range=today", nil)
	w := httptest.NewRecorder()
	s.handleStatement(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("[Fix12] esperado 200 para range=today, veio %d: %s", w.Code, w.Body.String())
	}
}

// [Fix 12] handleStatement aceita token "month" e retorna 200.
func TestHandleStatement_TokenMonth(t *testing.T) {
	now := time.Now().UTC()
	st := &fakeStatementStore{
		movements: []Movement{
			{ID: 2, Type: "boleto_in", Direction: "in", Method: "boleto", ValueCents: 999, Date: now.Add(-12 * time.Hour)},
		},
	}
	s := buildStatementServer(st)
	req := httptest.NewRequest(http.MethodGet, "/statement?range=month", nil)
	w := httptest.NewRecorder()
	s.handleStatement(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("[Fix12] esperado 200 para range=month, veio %d: %s", w.Code, w.Body.String())
	}
}

// errFake é um erro genérico para os testes de falha no store.
// Evita importar errors (pacote já importado por outros arquivos de teste no pacote).
type fakeError struct{ msg string }

func (e fakeError) Error() string { return e.msg }

var errFake = fakeError{"banco indisponível simulado"}
