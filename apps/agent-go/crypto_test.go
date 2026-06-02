package main

import "testing"

func TestEncryptDecryptRoundTrip(t *testing.T) {
	secret := "uma-chave-de-cifragem-bem-longa-e-aleatoria"
	plain := "sk-ant-oat01-exemplo-de-token-oauth"

	enc, err := encrypt(secret, plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if string(enc) == plain {
		t.Fatal("ciphertext igual ao plaintext")
	}
	got, err := decrypt(secret, enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != plain {
		t.Fatalf("esperava %q, veio %q", plain, got)
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	enc, _ := encrypt("chave-certa", "segredo")
	if _, err := decrypt("chave-errada", enc); err == nil {
		t.Fatal("decrypt deveria falhar com chave errada")
	}
}
