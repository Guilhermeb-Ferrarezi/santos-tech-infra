package main

import (
	"strings"
	"testing"
)

func TestNewAPIKey(t *testing.T) {
	token, prefix, hash := newAPIKey()

	if !strings.HasPrefix(token, "st_live_") {
		t.Fatalf("token sem prefixo esperado: %q", token)
	}
	if len(token) != len("st_live_")+48 {
		t.Fatalf("tamanho do token inesperado: %d", len(token))
	}
	if prefix != token[:16] {
		t.Errorf("prefix=%q não casa com o início do token", prefix)
	}
	if hash != sha256Hex(token) {
		t.Error("hash deveria ser o sha256 do token")
	}
	if hash == token {
		t.Error("hash não pode ser igual ao segredo")
	}

	// Dois tokens gerados devem ser distintos (segredo e hash).
	token2, _, hash2 := newAPIKey()
	if token == token2 || hash == hash2 {
		t.Error("tokens gerados deveriam ser únicos")
	}
}
