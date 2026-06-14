package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	emailVerifyTTL         = 10 * time.Minute
	emailVerifyCooldown    = 60 * time.Second
	emailVerifyMaxAttempts = 5 // teto de tentativas por código (anti brute-force dos 6 dígitos)
)

func emailVerifyKey(uid int64) string    { return fmt.Sprintf("email_verify:%d", uid) }
func emailVerifyCDKey(uid int64) string  { return fmt.Sprintf("email_verify_cd:%d", uid) }
func emailVerifyAttKey(uid int64) string { return fmt.Sprintf("email_verify_att:%d", uid) }

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
	if n, _ := s.rdb.Exists(r.Context(), emailVerifyCDKey(uid)).Result(); n == 1 {
		writeErr(w, appErr(http.StatusTooManyRequests, "RATE_LIMITED", "Aguarde um pouco antes de reenviar"))
		return
	}
	code := emailCode()
	if err := s.rdb.Set(r.Context(), emailVerifyKey(uid), code, emailVerifyTTL).Err(); err != nil {
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL", "Erro ao gerar o código"))
		return
	}
	s.rdb.Del(r.Context(), emailVerifyAttKey(uid))
	if err := s.rdb.Set(r.Context(), emailVerifyCDKey(uid), "1", emailVerifyCooldown).Err(); err != nil {
		slog.Warn("email_verify_send: falha ao gravar cooldown", "uid", uid, "err", err)
	}
	html := emailCodeHTML(code, "Use o código abaixo para verificar o seu email:")
	go func(to string) {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if err := s.email.send(ctx, to, "Verifique seu email — Santos Tech", html); err != nil {
			slog.Error("falha ao enviar email de verificação", "uid", uid, "err", err)
		}
	}(u.Email)
	writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}

// POST /auth/email-verify/confirm {code} — compara o código com o guardado no Redis
// (tempo constante, com teto de tentativas) e marca users.email_verified_at.
func (s *Server) handleEmailVerifyConfirm(w http.ResponseWriter, r *http.Request) {
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
	want, err := s.rdb.Get(r.Context(), emailVerifyKey(uid)).Result()
	if err != nil || want == "" {
		writeErr(w, appErr(http.StatusBadRequest, "CODE_EXPIRED", "Código expirado; envie outro"))
		return
	}
	// Teto de tentativas: o código de 6 dígitos não pode ser brute-forçável na janela.
	attempts, _ := s.rdb.Incr(r.Context(), emailVerifyAttKey(uid)).Result()
	if err := s.rdb.ExpireNX(r.Context(), emailVerifyAttKey(uid), emailVerifyTTL).Err(); err != nil {
		slog.Warn("email_verify_confirm: ExpireNX falhou; contador de tentativas pode não expirar", "uid", uid, "err", err)
	}
	if attempts > emailVerifyMaxAttempts {
		s.rdb.Del(r.Context(), emailVerifyKey(uid), emailVerifyAttKey(uid))
		writeErr(w, appErr(http.StatusTooManyRequests, "RATE_LIMITED", "Muitas tentativas; envie outro código"))
		return
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(want)) != 1 {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código incorreto"))
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE users SET email_verified_at=now() WHERE id=$1`, uid); err != nil {
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL", "Erro ao confirmar a verificação"))
		return
	}
	s.rdb.Del(r.Context(), emailVerifyKey(uid), emailVerifyAttKey(uid), emailVerifyCDKey(uid))
	writeJSON(w, http.StatusOK, map[string]bool{"verified": true})
}
