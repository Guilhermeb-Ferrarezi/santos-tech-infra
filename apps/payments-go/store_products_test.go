package main

import "testing"

func TestProductValid(t *testing.T) {
	if productValid(Product{Slug: "", Name: "x", PriceCents: 100}) == nil {
		t.Fatal("slug vazio deveria ser inválido")
	}
	if productValid(Product{Slug: "matricula", Name: "Matrícula", PriceCents: 0}) == nil {
		t.Fatal("preço 0 deveria ser inválido")
	}
	if err := productValid(Product{Slug: "matricula", Name: "Matrícula", PriceCents: 100}); err != nil {
		t.Fatalf("produto válido recusado: %v", err)
	}
}

func TestRecurringProductValid(t *testing.T) {
	d10 := 10
	d31 := 31
	// recurring com periodicity inválida → erro
	if productValid(Product{Slug: "p", Name: "P", PriceCents: 100, Recurring: true, Periodicity: "DIARIA", DueDay: &d10}) == nil {
		t.Fatal("periodicity inválida deveria ser recusada")
	}
	// recurring sem due_day → erro
	if productValid(Product{Slug: "p", Name: "P", PriceCents: 100, Recurring: true, Periodicity: "MENSAL", DueDay: nil}) == nil {
		t.Fatal("due_day ausente em produto recorrente deveria ser recusado")
	}
	// recurring com due_day fora de 1–28 → erro
	if productValid(Product{Slug: "p", Name: "P", PriceCents: 100, Recurring: true, Periodicity: "MENSAL", DueDay: &d31}) == nil {
		t.Fatal("due_day 31 deveria ser recusado")
	}
	// recurring válido
	if err := productValid(Product{Slug: "p", Name: "P", PriceCents: 100, Recurring: true, Periodicity: "MENSAL", DueDay: &d10}); err != nil {
		t.Fatalf("produto recorrente válido recusado: %v", err)
	}
	// não recorrente: periodicity/due_day ignorados
	if err := productValid(Product{Slug: "p", Name: "P", PriceCents: 100, Recurring: false}); err != nil {
		t.Fatalf("produto não recorrente válido recusado: %v", err)
	}
}
