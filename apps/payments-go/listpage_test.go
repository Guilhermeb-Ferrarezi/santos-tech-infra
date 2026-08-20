package main

import (
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

// TestPageFromRequest: nenhum caminho pode resultar em "sem limite" — era exatamente
// a ausência de LIMIT que fazia a listagem carregar a tabela inteira.
func TestPageFromRequest(t *testing.T) {
	casos := []struct {
		query      string
		wantLimit  int32
		wantBefore int64
	}{
		{"", defaultListLimit, 0},
		{"?limit=50", 50, 0},
		{"?limit=99999", maxListLimit, 0},   // teto absoluto
		{"?limit=0", defaultListLimit, 0},   // inválido → padrão
		{"?limit=-5", defaultListLimit, 0},  // inválido → padrão
		{"?limit=abc", defaultListLimit, 0}, // inválido → padrão
		{"?before=1234", defaultListLimit, 1234},
		{"?before=0", defaultListLimit, 0},
		{"?before=-1", defaultListLimit, 0},
		{"?before=xyz", defaultListLimit, 0},
		{"?limit=10&before=77", 10, 77},
	}
	for _, c := range casos {
		p := pageFromRequest(httptest.NewRequest("GET", "/charges"+c.query, nil))
		if p.Limit != c.wantLimit || p.BeforeID != c.wantBefore {
			t.Errorf("%q: esperado limit=%d before=%d, veio limit=%d before=%d",
				c.query, c.wantLimit, c.wantBefore, p.Limit, p.BeforeID)
		}
		if p.Limit <= 0 {
			t.Errorf("%q: limit nunca pode ser <= 0 (viraria listagem sem teto)", c.query)
		}
		if p.Limit > maxListLimit {
			t.Errorf("%q: limit passou do teto absoluto", c.query)
		}
	}
}

// TestSortMovements_EstavelEDecrescente: ordem decrescente por data e estabilidade entre datas iguais
// (o insertion sort antigo era estável; sort.SliceStable mantém isso).
func TestSortMovements_EstavelEDecrescente(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	mvs := []Movement{
		{ID: 1, Type: "pix_in", Date: base},
		{ID: 2, Type: "payout", Date: base.Add(2 * time.Hour)},
		{ID: 3, Type: "boleto_in", Date: base.Add(-time.Hour)},
		{ID: 4, Type: "pix_in", Date: base}, // empate com o ID 1
		{ID: 5, Type: "pix_in", Date: base.Add(3 * time.Hour)},
	}
	sortMovements(mvs)

	if !sort.SliceIsSorted(mvs, func(i, j int) bool { return mvs[i].Date.After(mvs[j].Date) }) {
		t.Fatalf("movimentos fora de ordem decrescente: %+v", mvs)
	}
	if mvs[0].ID != 5 || mvs[len(mvs)-1].ID != 3 {
		t.Fatalf("extremos errados: primeiro=%d último=%d", mvs[0].ID, mvs[len(mvs)-1].ID)
	}
	// Estabilidade: entre os empatados, 1 continua antes de 4.
	var pos1, pos4 int
	for i, m := range mvs {
		if m.ID == 1 {
			pos1 = i
		}
		if m.ID == 4 {
			pos4 = i
		}
	}
	if pos1 > pos4 {
		t.Fatalf("ordenação deveria ser estável entre datas iguais (1 antes de 4), veio %d/%d", pos1, pos4)
	}
}

// TestSortMovements_Grande garante que uma janela grande de extrato não depende mais
// do insertion sort O(n²) — com 200 mil movimentos ele levaria minutos.
func TestSortMovements_Grande(t *testing.T) {
	const n = 200000
	base := time.Now()
	mvs := make([]Movement, n)
	for i := range mvs {
		// Ordem crescente na entrada = pior caso do insertion sort.
		mvs[i] = Movement{ID: int64(i), Date: base.Add(time.Duration(i) * time.Second)}
	}
	done := make(chan struct{})
	go func() {
		sortMovements(mvs)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sortMovements não terminou em 10s — voltou a ser O(n²)?")
	}
	if !mvs[0].Date.After(mvs[n-1].Date) {
		t.Fatal("ordem decrescente quebrada")
	}
}
