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

// randomDigits usa amostragem com rejeição: a distribuição dos dígitos tem que
// ser uniforme. Com byte%10 direto (o bug antigo), 0-5 saíam ~4% mais que 6-9 —
// o que aparece como qui-quadrado alto nessa amostra.
func TestRandomDigitsUniform(t *testing.T) {
	const draws = 20000
	var counts [10]int
	for i := 0; i < draws/10; i++ {
		code := randomDigits(10)
		if len(code) != 10 {
			t.Fatalf("randomDigits(10) devolveu %d chars: %q", len(code), code)
		}
		for _, c := range code {
			if c < '0' || c > '9' {
				t.Fatalf("caractere não-numérico em %q", code)
			}
			counts[c-'0']++
		}
	}
	expected := float64(draws) / 10
	var chi2 float64
	for _, c := range counts {
		d := float64(c) - expected
		chi2 += d * d / expected
	}
	// 9 graus de liberdade: o valor crítico a 0.1% é 27.88. O viés antigo dava
	// um chi2 na casa das centenas com esse tamanho de amostra.
	if chi2 > 27.88 {
		t.Errorf("distribuição enviesada: chi2=%.1f, contagens=%v", chi2, counts)
	}
}

func TestRandomDigitsLengths(t *testing.T) {
	for _, n := range []int{0, 1, 6, 13, 64} {
		if got := randomDigits(n); len(got) != n {
			t.Errorf("randomDigits(%d) = %q (len %d)", n, got, len(got))
		}
	}
}
