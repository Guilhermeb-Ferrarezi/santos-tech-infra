package main

import (
	"encoding/hex"
	"testing"
)

func TestRandomToken(t *testing.T) {
	a := randomToken(16)
	if len(a) != 32 { // 16 bytes → 32 hex chars
		t.Fatalf("len = %d, queria 32", len(a))
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Fatalf("não é hex válido: %v", err)
	}
	if a == randomToken(16) {
		t.Fatal("dois tokens consecutivos iguais")
	}
}

func TestHashRefreshToken(t *testing.T) {
	const in = "meu-refresh-token"
	h := hashRefreshToken(in)
	if h != hashRefreshToken(in) {
		t.Error("hash não é determinístico")
	}
	if len(h) != 64 { // sha256 → 64 hex chars
		t.Errorf("len = %d, queria 64", len(h))
	}
	if hashRefreshToken("outro-token") == h {
		t.Error("tokens diferentes geraram o mesmo hash")
	}
}

func TestSha256HexKnownVector(t *testing.T) {
	// vetor conhecido: sha256("abc")
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := sha256Hex("abc"); got != want {
		t.Errorf("sha256Hex(abc) = %s, queria %s", got, want)
	}
}
