package main

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestVerifyPKCE(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])

	if !verifyPKCE(verifier, challenge) {
		t.Fatal("verifier correto deveria validar")
	}
	if verifyPKCE("outro-verifier", challenge) {
		t.Fatal("verifier errado não deveria validar")
	}
	if verifyPKCE("", challenge) || verifyPKCE(verifier, "") {
		t.Fatal("vazio não deveria validar")
	}
}

func TestAuthCodeKeyHashesCode(t *testing.T) {
	if authCodeKey("abc") == "oauth:code:abc" {
		t.Fatal("a chave deve usar o hash do code, nunca o code em claro")
	}
	if authCodeKey("abc") != "oauth:code:"+sha256Hex("abc") {
		t.Fatal("chave inesperada")
	}
}
