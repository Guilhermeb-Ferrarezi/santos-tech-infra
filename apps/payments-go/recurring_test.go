package main

import (
	"testing"
	"time"
)

func TestMonthlyDueDate(t *testing.T) {
	if got := monthlyDueDate(2026, time.June, 10); got != "2026-06-10" {
		t.Fatalf("esperava 2026-06-10, veio %s", got)
	}
	if d := monthlyDueDate(2026, time.February, 28); d != "2026-02-28" {
		t.Fatalf("esperava 2026-02-28, veio %s", d)
	}
}

func TestNextDueDate(t *testing.T) {
	// due_day ainda à frente neste mês → mesmo mês.
	if got := nextDueDate(10, time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)).Format("2006-01-02"); got != "2026-06-10" {
		t.Fatalf("esperava 2026-06-10, veio %s", got)
	}
	// due_day já passou (ou é hoje) → próximo mês (futuro estrito).
	if got := nextDueDate(10, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)).Format("2006-01-02"); got != "2026-07-10" {
		t.Fatalf("esperava 2026-07-10, veio %s", got)
	}
	if got := nextDueDate(10, time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)).Format("2006-01-02"); got != "2026-07-10" {
		t.Fatalf("esperava 2026-07-10, veio %s", got)
	}
	// virada de ano (dezembro → janeiro).
	if got := nextDueDate(5, time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC)).Format("2006-01-02"); got != "2027-01-05" {
		t.Fatalf("esperava 2027-01-05, veio %s", got)
	}
}

func TestRecurringTxid(t *testing.T) {
	got := recurringTxid(123, "2026-08")
	if want := "stprec0000000000000000123202608"; got != want {
		t.Fatalf("txid: esperava %q, veio %q", want, got)
	}
	// Determinístico: mesma (rec, ciclo) → mesmo txid (garante idempotência do PUT na Efí).
	if again := recurringTxid(123, "2026-08"); again != got {
		t.Fatalf("txid não determinístico: %q != %q", again, got)
	}
	// Ciclo diferente → txid diferente.
	if recurringTxid(123, "2026-09") == got {
		t.Fatal("txid não deveria colidir entre ciclos diferentes")
	}
	// Dentro das regras da Efí: 26–35 chars, só alfanuméricos.
	if n := len(got); n < 26 || n > 35 {
		t.Fatalf("txid fora de 26–35 chars: %d (%q)", n, got)
	}
	for _, r := range got {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			t.Fatalf("txid com char não-alfanumérico %q em %q", r, got)
		}
	}
}
