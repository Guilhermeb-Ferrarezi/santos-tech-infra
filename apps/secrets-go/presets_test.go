package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidPresetID(t *testing.T) {
	if id := newPresetID(); !validPresetID(id) {
		t.Fatalf("id gerado deveria ser válido: %q", id)
	}
	bad := []string{
		"",
		"abc",
		"x?refresh=true",
		"aaaaaaaaaaaaaaaaaaaaaaa",   // 23
		"aaaaaaaaaaaaaaaaaaaaaaaaa", // 25
		"AAAAAAAAAAAAAAAAAAAAAAAA",  // maiúsculo
		"../_cluster/settings0000",
	}
	for _, id := range bad {
		if validPresetID(id) {
			t.Errorf("id inválido aceito: %q", id)
		}
	}
}

// Um id fora do formato nunca pode virar uma chamada ao Elasticsearch.
func TestDeletePresetRejeitaIDInjetado(t *testing.T) {
	var called bool
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer es.Close()

	client := NewElasticClient(es.URL, nil)
	if err := client.DeletePreset(context.Background(), "x?refresh=true"); err != errInvalidPresetID {
		t.Fatalf("esperava errInvalidPresetID, veio %v", err)
	}
	if err := client.PutPreset(context.Background(), KeywordPreset{ID: "../../_all/_doc/1"}); err != errInvalidPresetID {
		t.Fatalf("esperava errInvalidPresetID no put, veio %v", err)
	}
	if called {
		t.Fatal("o Elasticsearch chegou a ser chamado com id inválido")
	}
}
