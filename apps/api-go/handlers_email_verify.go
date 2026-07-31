package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	emailVerifyTTL         = 10 * time.Minute
	emailVerifyCooldown    = 60 * time.Second
	emailVerifyMaxAttempts = 5 // teto de tentativas por código (anti brute-force dos 6 dígitos)
)

func emailVerifyKey(uid int64) string    { return fmt.Sprintf("email_verify:%d", uid) }
func emailVerifyCDKey(uid int64) string  { return fmt.Sprintf("email_verify_cd:%d", uid) }
func emailVerifyAttKey(uid int64) string { return fmt.Sprintf("api-go:email_verify_att:%d", uid) }

// POST /auth/email-verify/send — gera um código de 6 dígitos, guarda no Redis
// (TTL 10min) e envia pro email da conta. Cooldown de 60s entre envios.
func (s *Server) handleEmailVerifySend(w http.ResponseWriter, r *http.Request) {
	uid := userIDFrom(r)
	u, err := s.userByID(r.Context(), uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil || u.Email == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "Conta sem email"))
		return
	}
	if u.EmailVerifiedAt != nil {
		writeErr(w, appErr(http.StatusConflict, "ALREADY_VERIFIED", "Este email já foi verificado"))
		return
	}
	// SetNX é atômico: exatamente uma requisição concorrente "adquire" o slot de
	// envio. O par Exists + Set não-atômico deixava uma janela onde duas chamadas
	// simultâneas passavam pelo check e enviavam dois emails, sobrescrevendo o código.
	acquired, errCD := s.rdb.SetNX(r.Context(), emailVerifyCDKey(uid), "1", emailVerifyCooldown).Result()
	if errCD != nil {
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL", "Erro ao verificar cooldown"))
		return
	}
	if !acquired {
		writeErr(w, appErr(http.StatusTooManyRequests, "RATE_LIMITED", "Aguarde um pouco antes de reenviar"))
		return
	}
	code := emailCode()
	if err := s.rdb.Set(r.Context(), emailVerifyKey(uid), code, emailVerifyTTL).Err(); err != nil {
		// Desfaz o cooldown para que o usuário possa retentar imediatamente.
		_ = s.rdb.Del(r.Context(), emailVerifyCDKey(uid))
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL", "Erro ao gerar o código"))
		return
	}
	if err := s.rdb.Del(r.Context(), emailVerifyAttKey(uid)).Err(); err != nil {
		slog.Warn("email_verify_send: falha ao limpar contador de tentativas no Redis", "uid", uid, "err", err)
	}
	html := emailCodeHTML(code, "Use o código abaixo para verificar o seu email:")
	s.enqueueEmail("handleEmailVerifySend", u.Email, "Verifique seu email — Santos Tech", html)
	writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}

// POST /auth/email-verify/confirm {code} — compara o código com o guardado no Redis
// (tempo constante, com teto de tentativas) e marca users.email_verified_at.
func (s *Server) handleEmailVerifyConfirm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	uid := userIDFrom(r)
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	code := strings.TrimSpace(body.Code)
	if len(code) != 6 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "Código inválido"))
		return
	}
	// Teto de tentativas: incrementa o contador ANTES de ler o código — padrão
	// idêntico ao handleMFAVerify, handleMFAEnable e handleSudoVerify. Assim,
	// toda requisição (correta ou não) consome um slot do limite antes de acessar
	// o código, fechando a janela onde requisições concorrentes conseguiam ler
	// o código antes de qualquer INCR poder bloqueá-las (GET-after-INCR fecha
	// esse TOCTOU de concorrência). fail-closed: Redis indisponível → 500.
	attemptsCmd := s.rdb.Incr(r.Context(), emailVerifyAttKey(uid))
	if attemptsCmd.Err() != nil {
		slog.Warn("email_verify_confirm: redis error; rejecting to fail closed", "uid", uid, "err", attemptsCmd.Err())
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL", "Erro interno. Tente novamente."))
		return
	}
	attempts := attemptsCmd.Val()
	if err := s.rdb.ExpireNX(r.Context(), emailVerifyAttKey(uid), emailVerifyTTL).Err(); err != nil {
		slog.Warn("email_verify_confirm: ExpireNX falhou; contador de tentativas pode não expirar", "uid", uid, "err", err)
	}
	if attempts > emailVerifyMaxAttempts {
		if err := s.rdb.Del(r.Context(), emailVerifyKey(uid), emailVerifyAttKey(uid)).Err(); err != nil {
			slog.Warn("email_verify_confirm: falha ao invalidar código após exceder tentativas", "uid", uid, "err", err)
		}
		writeErr(w, appErr(http.StatusTooManyRequests, "RATE_LIMITED", "Muitas tentativas; envie outro código"))
		return
	}
	want, err := s.rdb.Get(r.Context(), emailVerifyKey(uid)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			writeErr(w, appErr(http.StatusBadRequest, "CODE_EXPIRED", "Código expirado; envie outro"))
			return
		}
		slog.Warn("email_verify_confirm: redis error ao obter código", "uid", uid, "err", err)
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL", "Erro interno. Tente novamente."))
		return
	}
	if want == "" {
		writeErr(w, appErr(http.StatusBadRequest, "CODE_EXPIRED", "Código expirado; envie outro"))
		return
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(want)) != 1 {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código incorreto"))
		return
	}
	// Invalida o código ANTES de gravar no banco (fail-closed): se Del falhar, o
	// código continua válido no Redis mas o banco não é atualizado, permitindo nova
	// tentativa. A ordem inversa (DB primeiro) deixaria o código reutilizável após
	// uma verificação bem-sucedida — padrão espelhado do handleMFAVerify.
	if err := s.rdb.Del(r.Context(), emailVerifyKey(uid), emailVerifyAttKey(uid), emailVerifyCDKey(uid)).Err(); err != nil {
		slog.Error("email_verify_confirm: falha ao invalidar código após verificação bem-sucedida", "uid", uid, "err", err)
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL", "Erro ao confirmar a verificação. Tente novamente."))
		return
	}
	if err := s.setEmailVerified(r.Context(), uid); err != nil {
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL", "Erro ao confirmar a verificação"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"verified": true})
}
