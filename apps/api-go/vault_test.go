package main

import "testing"

func TestVaultEncryptDecryptRoundTrip(t *testing.T) {
	v := newVault("segredo-de-teste-123")
	plaintext := "sk-live-abcdef1234567890"

	enc, err := v.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if enc == plaintext {
		t.Fatal("valor cifrado igual ao plaintext")
	}

	dec, err := v.Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if dec != plaintext {
		t.Fatalf("dec=%q, queria %q", dec, plaintext)
	}
}

func TestVaultDecryptComChaveErradaFalha(t *testing.T) {
	v1 := newVault("chave-um")
	v2 := newVault("chave-dois")

	enc, err := v1.Encrypt("segredo")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := v2.Decrypt(enc); err == nil {
		t.Fatal("esperava erro ao decriptar com chave errada")
	}
}

func TestNewVaultSecretVazioDevolveNil(t *testing.T) {
	if v := newVault(""); v != nil {
		t.Fatalf("newVault(\"\") = %v, queria nil (feature desabilitada)", v)
	}
}

func TestMaskSecret(t *testing.T) {
	cases := map[string]string{
		"sk-live-abcdef1234567890": "7890",
		"ab":                       "",
		"":                         "",
	}
	for in, want := range cases {
		if got := maskSecret(in); got != want {
			t.Errorf("maskSecret(%q) = %q, queria %q", in, got, want)
		}
	}
}
