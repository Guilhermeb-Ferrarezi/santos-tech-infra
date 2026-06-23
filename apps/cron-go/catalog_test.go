package main

import "testing"

func TestCatalogLookupKnown(t *testing.T) {
	a, ok := lookupCatalog("payments.gerar-cobrancas-mes")
	if !ok {
		t.Fatal("esperava encontrar a ação do catálogo")
	}
	if a.Method == "" || a.Host == "" || a.Path == "" {
		t.Fatalf("ação incompleta: %+v", a)
	}
}

func TestCatalogLookupUnknown(t *testing.T) {
	if _, ok := lookupCatalog("nao.existe"); ok {
		t.Fatal("não deveria encontrar ação inexistente")
	}
}
