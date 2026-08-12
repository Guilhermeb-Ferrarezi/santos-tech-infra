package main

import (
	"context"
	"testing"
	"time"
)

// TestDotfyVerifier_RejectsGarbageKey chama a API real da Dotfy (rede) com
// uma chave inválida — confirma que o healthcheck responde 401 como esperado
// pela documentação, sem gastar nenhuma chave real.
func TestDotfyVerifier_RejectsGarbageKey(t *testing.T) {
	if testing.Short() {
		t.Skip("pula chamada de rede em -short")
	}
	v := NewDotfyVerifier()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	checked, active := v.CheckKey(ctx, "vk_test_000000000000000000000000000000invalid")
	if !checked {
		t.Fatalf("esperava checked=true (API respondeu), mas deu erro de rede/timeout")
	}
	if active {
		t.Fatalf("esperava active=false pra chave inventada, mas API disse que é válida")
	}
}
