package main

import "testing"

func TestNormalizeKind(t *testing.T) {
	for _, in := range []string{"", "chat"} {
		got, err := normalizeKind(in)
		if err != nil || got != chatKind {
			t.Fatalf("normalizeKind(%q) = %q, %v; queria %q, nil", in, got, err, chatKind)
		}
	}
	got, err := normalizeKind("design")
	if err != nil || got != designKind {
		t.Fatalf("normalizeKind(design) = %q, %v", got, err)
	}
	if _, err := normalizeKind("outro"); err == nil {
		t.Fatal("kind desconhecido deveria falhar")
	}
	// Espaço em volta não deve virar kind inválido.
	if got, err := normalizeKind("  design  "); err != nil || got != designKind {
		t.Fatalf("kind com espaços = %q, %v", got, err)
	}
}
