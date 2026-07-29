package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"runtime/debug"
)

// safeGo roda fn numa goroutine fire-and-forget com recover() como primeiro defer,
// logando qualquer panic (com stack) em vez de derrubar o processo inteiro. Usado
// para envios de email assíncronos e afins.
func safeGo(name string, fn func()) {
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic em goroutine", "where", name, "panic", rec, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}

func randomToken(nbytes int) string {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// isValidChallenge reports whether s is a well-formed MFA challenge token —
// the exact output of randomToken(24): 48 lowercase hex characters.
// Validates input before it reaches Redis key construction.
func isValidChallenge(s string) bool {
	if len(s) != 48 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// isValidResetToken reports whether s is a well-formed password-reset token —
// the exact output of randomToken(32): 64 lowercase hex characters.
// Validates input before it reaches Redis key construction.
func isValidResetToken(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// isValidOAuthRequestID reports whether s is a well-formed OAuth authorization
// request ID — the exact output of randomToken(16): 32 lowercase hex characters.
// Validates input before it reaches Redis key construction (mirrors isValidChallenge
// for MFA challenges and isValidResetToken for password-reset tokens).
func isValidOAuthRequestID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// recoveryCodeLen é o comprimento esperado de um código de recuperação MFA:
// base32 sem padding de 8 bytes aleatórios = exatamente 13 caracteres A-Z2-7.
const recoveryCodeLen = 13

// truncateRunes truncates s to at most max Unicode code points (runes).
// Unlike s[:n], it never cuts through a multi-byte character.
func truncateRunes(s string, max int) string {
	if runes := []rune(s); len(runes) > max {
		return string(runes[:max])
	}
	return s
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
