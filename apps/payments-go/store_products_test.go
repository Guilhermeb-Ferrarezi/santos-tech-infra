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
