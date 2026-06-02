package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

func randomToken(nbytes int) string {
	b := make([]byte, nbytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func hashRefreshToken(token string) string { return sha256Hex(token) }
