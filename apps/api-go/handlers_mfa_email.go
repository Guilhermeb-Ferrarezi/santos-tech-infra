package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// 2FA por email da CONTA logada (ativar/desativar — diferente do código do 2º passo
// do login, que é preso ao challenge). Chave por usuário, TTL curto, cooldown.
const (
	mfaEmailAcctTTL      = 10 * time.Minute
	mfaEmailAcctCooldown = 60 * time.Second
)

func mfaEmailAcctKey(uid int64) string   { return fmt.Sprintf("mfa_email_acct:%d", uid) }
func mfaEmailAcctCDKey(uid int64) string { return fmt.Sprintf("mfa_email_acct_cd:%d", uid) }

// sendChallengeEmailCode gera e envia o código do 2º passo preso a um challenge de
// login. Reusado pelo login por senha, pelo OAuth (método preferido = email) e pelo
// reenvio explícito (/auth/mfa/email).
func (s *Server) sendChallengeEmailCode(ctx context.Context, challenge, email string) error {
	code := emailCode()
	if err := s.rdb.Set(ctx, "mfa_email:"+challenge, code, 10*time.Minute).Err(); err != nil {
		return err
	}
	html := emailCodeHTML(code, "Use o código abaixo para concluir o seu login:")
	go func(to string) {
		c, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		_ = s.email.send(c, to, "Seu código de verificação — Santos Tech", html)
	}(email)
	return nil
}

// mfaMethods devolve os métodos de 2º fator disponíveis para o usuário.
func mfaMethods(u *User) []string {
	var m []string
	if u.TOTPSecret != nil {
		m = append(m, "totp")
	}
	if u.EmailVerifiedAt != nil {
		m = append(m, "email")
	}
	return m
}

// POST /auth/mfa/email-code — envia um código pro email da conta logada (usado para
// ativar/desativar o 2FA por email). Cooldown de 60s.
func (s *Server) handleMFAEmailCode(w http.ResponseWriter, r *http.Request) {
	uid := userIDFrom(r)
	u, err := s.userByID(r.Context(), uid)
	if err != nil || u == nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Não autenticado"))
		return
	}
	if u.EmailVerifiedAt == nil {
		writeErr(w, appErr(http.StatusConflict, "EMAIL_NOT_VERIFIED", "Verifique seu email antes de usá-lo como 2º fator"))
		return
	}
	if n, _ := s.rdb.Exists(r.Context(), mfaEmailAcctCDKey(uid)).Result(); n == 1 {
		writeErr(w, appErr(http.StatusTooManyRequests, "RATE_LIMITED", "Aguarde um pouco antes de reenviar"))
		return
	}
	code := emailCode()
	if err := s.rdb.Set(r.Context(), mfaEmailAcctKey(uid), code, mfaEmailAcctTTL).Err(); err != nil {
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL", "Erro ao gerar o código"))
		return
	}
	s.rdb.Set(r.Context(), mfaEmailAcctCDKey(uid), "1", mfaEmailAcctCooldown)
	html := emailCodeHTML(code, "Use o código abaixo para confirmar a alteração do 2FA da sua conta:")
	go func(to string) {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		_ = s.email.send(ctx, to, "Seu código de verificação — Santos Tech", html)
	}(u.Email)
	writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}

// consumeAcctEmailCode valida (tempo constante) e consome o código enviado pro email
// da conta. One-shot: válido uma única vez.
func (s *Server) consumeAcctEmailCode(ctx context.Context, uid int64, code string) bool {
	want, err := s.rdb.Get(ctx, mfaEmailAcctKey(uid)).Result()
	if err != nil || want == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(code)), []byte(want)) != 1 {
		return false
	}
	s.rdb.Del(ctx, mfaEmailAcctKey(uid))
	return true
}

// POST /auth/mfa/email-enable {code} — ativa o 2FA por email: exige email verificado
// e o código recém-enviado (/auth/mfa/email-code). Se for o primeiro método, vira o
// preferido; gera códigos de recuperação na primeira ativação do MFA.
func (s *Server) handleMFAEmailEnable(w http.ResponseWriter, r *http.Request) {
	uid := userIDFrom(r)
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	u, err := s.userByID(r.Context(), uid)
	if err != nil || u == nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Não autenticado"))
		return
	}
	if u.EmailVerifiedAt == nil {
		writeErr(w, appErr(http.StatusConflict, "EMAIL_NOT_VERIFIED", "Verifique seu email antes de usá-lo como 2º fator"))
		return
	}
	if !s.consumeAcctEmailCode(r.Context(), uid, body.Code) {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código inválido ou expirado"))
		return
	}
	method := u.MFAMethod
	if u.TOTPSecret == nil || !u.MFAEnabled {
		method = "email" // primeiro (ou único) método vira o preferido
	}
	if _, err := s.db.Exec(r.Context(),
		`UPDATE users SET mfa_enabled=true, mfa_method=$1 WHERE id=$2`, method, uid); err != nil {
		writeErr(w, err)
		return
	}
	// Recovery codes só na primeira ativação do MFA (não regenera ao somar método).
	resp := map[string]any{"enabled": true, "method": method}
	if !u.MFAEnabled {
		codes := genRecoveryCodes(10)
		hashes := make([]string, len(codes))
		for i, c := range codes {
			hashes[i] = sha256Hex(c)
		}
		_ = s.deleteRecoveryCodes(r.Context(), uid)
		if err := s.insertRecoveryCodes(r.Context(), uid, hashes); err != nil {
			writeErr(w, err)
			return
		}
		resp["recoveryCodes"] = codes
	}
	writeJSON(w, http.StatusOK, resp)
}

// POST /auth/mfa/method {method} — define o método preferido do 2º passo ('totp' ou
// 'email'). Só aceita método de fato disponível na conta.
func (s *Server) handleMFAMethod(w http.ResponseWriter, r *http.Request) {
	uid := userIDFrom(r)
	var body struct {
		Method string `json:"method"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	u, err := s.userByID(r.Context(), uid)
	if err != nil || u == nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Não autenticado"))
		return
	}
	switch body.Method {
	case "totp":
		if u.TOTPSecret == nil {
			writeErr(w, appErr(http.StatusConflict, "METHOD_UNAVAILABLE", "Configure o app autenticador primeiro"))
			return
		}
	case "email":
		if u.EmailVerifiedAt == nil {
			writeErr(w, appErr(http.StatusConflict, "METHOD_UNAVAILABLE", "Verifique seu email primeiro"))
			return
		}
	default:
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "Método inválido"))
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE users SET mfa_method=$1 WHERE id=$2`, body.Method, uid); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"method": body.Method})
}
