package main

import "testing"

func TestHashVerifyRoundtrip(t *testing.T) {
	h, err := hashPassword("minha-senha-secreta")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !verifyPassword("minha-senha-secreta", h) {
		t.Error("senha correta não verificou")
	}
	if verifyPassword("senha-errada", h) {
		t.Error("senha errada verificou")
	}
}

// Compatibilidade argon2id: este hash foi gerado pelo Bun.password.hash(...)
// (o auth antigo era em TS/Bun). O Go precisa validá-lo sem rehash.
func TestVerifyBunHash(t *testing.T) {
	const bunHash = "$argon2id$v=19$m=65536,t=2,p=1$KdOikuizeBmEceTxWMOxA5v6fcz2wmGs9FdO1Afmdbk$iLbhUXEl4CKZz1VaJTZjrHxcGfNZpTnFsFVulG32+74"
	if !verifyPassword("senha-de-teste-123", bunHash) {
		t.Error("hash do Bun não verificou no Go (quebra de compatibilidade)")
	}
	if verifyPassword("senha-errada", bunHash) {
		t.Error("senha errada passou no hash do Bun")
	}
}

func TestVerifyPasswordGarbage(t *testing.T) {
	if verifyPassword("x", "não-é-um-hash-phc") {
		t.Error("hash inválido não deveria verificar")
	}
}
