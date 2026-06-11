package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

func randomToken(nbytes int) string {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func hashRefreshToken(token string) string { return sha256Hex(token) }

// newAPIKey gera um Personal Access Token: o segredo completo (mostrado uma única
// vez ao usuário), um prefixo curto para exibição na listagem e o hash SHA-256 —
// é o hash, não o segredo, que fica guardado no banco.
func newAPIKey() (token, prefix, hash string) {
	token = "st_live_" + randomToken(24) // 24 bytes => 48 hex chars
	prefix = token[:16]                  // "st_live_" + 8 hex
	hash = sha256Hex(token)
	return
}
