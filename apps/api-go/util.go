package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
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

// maskForLog devolve uma forma reconhecível — mas inútil para quem a lê — de um
// valor sensível (challenge de MFA, token de reset, código). Mantém os 6
// primeiros caracteres e o comprimento: o bastante para correlacionar linhas de
// log sem publicar a credencial inteira no Loki, que os admins consultam pela
// própria API (/auth/admin/logs). Difere do maskSecret do vault.go, que serve
// para EXIBIR um segredo no frontend (últimos 4 caracteres).
func maskForLog(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 6 {
		return "***"
	}
	return s[:6] + "***(" + strconv.Itoa(len(s)) + ")"
}

func randomToken(nbytes int) string {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// randomDigits gera um código numérico de n dígitos (crypto/rand) — pro código
// curto do QR login e do pareamento (digitável à mão, ao contrário do token hex).
//
// Usa amostragem com rejeição porque 256 não é múltiplo de 10: mapear byte%10
// direto faz os dígitos 0-5 saírem em 26 dos 256 valores e os 6-9 em apenas 25
// (~4% de viés). Descartando os bytes >= 250 sobram 250 valores, exatamente 25
// por dígito — distribuição uniforme de verdade.
func randomDigits(n int) string {
	const digits = "0123456789"
	const maxUnbiased = 250 // 25 * 10; bytes >= 250 são descartados
	out := make([]byte, 0, n)
	buf := make([]byte, n+n/4+8) // folga para os bytes rejeitados
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}
		for _, v := range buf {
			if v >= maxUnbiased {
				continue
			}
			out = append(out, digits[v%10])
			if len(out) == n {
				break
			}
		}
	}
	return string(out)
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

// validateAssigneeIDs confere que cada id de responsável adicional (tasks.assigneeIds /
// socialPosts.assigneeIds) corresponde a um usuário existente, devolvendo um 400 claro
// em vez de deixar a FK de task_assignees/social_post_assignees estourar como 500 —
// mesma convenção de handleSetSocialPlatformOwner para o dono de plataforma.
func (s *Server) validateAssigneeIDs(ctx context.Context, ids []int64) error {
	for _, id := range ids {
		u, err := s.cachedUserByID(ctx, id)
		if err != nil {
			return err
		}
		if u == nil {
			return appErr(http.StatusBadRequest, "BAD_REQUEST", "Responsável adicional inválido")
		}
	}
	return nil
}

// newAPIKey gera um Personal Access Token: o segredo completo (mostrado uma única
// vez ao usuário), um prefixo curto para exibição na listagem e o hash SHA-256 —
// é o hash, não o segredo, que fica guardado no banco.
func newAPIKey() (token, prefix, hash string) {
	token = "st_live_" + randomToken(24) // 24 bytes => 48 hex chars
	prefix = token[:16]                  // "st_live_" + 8 hex
	hash = sha256Hex(token)
	return
}
