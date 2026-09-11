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
		// checked=false é documentado em CheckKey como "erro de rede/timeout —
		// não interpretar como chave inválida". O runner do GitHub Actions não
		// tem alcance garantido a todo endpoint de terceiro, então pula em vez
		// de quebrar o CI (mesmo tratamento de TestGenericVerifier_RejectsGarbageKeys
		// em verifiers_test.go).
		t.Skip("app.dotfy.com.br não respondeu do runner (rede bloqueada/timeout) — pulando")
	}
	if active {
		t.Fatalf("esperava active=false pra chave inventada, mas API disse que é válida")
	}
}
