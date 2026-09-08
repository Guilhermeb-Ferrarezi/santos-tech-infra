package main

import (
	"os"
	"testing"
)

func lerArquivo(t *testing.T, caminho string) string {
	t.Helper()
	b, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("não consegui ler %s: %v", caminho, err)
	}
	return string(b)
}
