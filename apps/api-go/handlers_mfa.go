package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pquerna/otp/totp"
)

// POST /auth/mfa/setup — gera um secret TOTP pendente + otpauth URL (QR).
func (s *Server) handleMFASetup(w http.ResponseWriter, r *http.Request) {
	uid := userIDFrom(r)
	u, err := s.userByID(r.Context(), uid)
	if err != nil || u == nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Não autenticado"))
		return
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Santos Tech", AccountName: u.Email})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.rdb.Set(r.Context(), "mfa_setup:"+strconv.FormatInt(uid, 10), key.Secret(), 10*time.Minute)
	writeJSON(w, http.StatusOK, map[string]string{"secret": key.Secret(), "otpauthUrl": key.URL()})
}

// POST /auth/mfa/enable {code} — confirma o TOTP e ativa o MFA, devolve recovery codes.
func (s *Server) handleMFAEnable(w http.ResponseWriter, r *http.Request) {
	uid := userIDFrom(r)
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	secret, err := s.rdb.Get(r.Context(), "mfa_setup:"+strconv.FormatInt(uid, 10)).Result()
	if err != nil || secret == "" {
		writeErr(w, appErr(http.StatusBadRequest, "MFA_SETUP_EXPIRED", "Setup expirado, gere novamente"))
		return
	}
	if !totp.Validate(body.Code, secret) {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código inválido"))
		return
	}
	if err := s.setMFA(r.Context(), uid, true, &secret); err != nil {
		writeErr(w, err)
		return
	}
	s.rdb.Del(r.Context(), "mfa_setup:"+strconv.FormatInt(uid, 10))

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
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "recoveryCodes": codes})
}

// POST /auth/mfa/disable {code} — desativa (aceita TOTP, recovery ou código por email).
func (s *Server) handleMFADisable(w http.ResponseWriter, r *http.Request) {
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
	if !u.MFAEnabled {
		writeJSON(w, http.StatusOK, map[string]bool{"disabled": true})
		return
	}
	// Aceita qualquer fator válido da conta: TOTP, código de recuperação ou o código
	// enviado pro email (/auth/mfa/email-code) — necessário p/ contas só com email-2FA.
	code := strings.TrimSpace(body.Code)
	valid := u.TOTPSecret != nil && totp.Validate(code, *u.TOTPSecret)
	if !valid {
		valid = s.consumeRecoveryCode(r.Context(), uid, sha256Hex(strings.ToUpper(code)))
	}
	if !valid {
		valid = s.consumeAcctEmailCode(r.Context(), uid, code)
	}
	if !valid {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código inválido"))
		return
	}
	if err := s.setMFA(r.Context(), uid, false, nil); err != nil {
		writeErr(w, err)
		return
	}
	_ = s.deleteRecoveryCodes(r.Context(), uid)
	writeJSON(w, http.StatusOK, map[string]bool{"disabled": true})
}

// POST /auth/mfa/email {challenge} — envia um código OTP por email (2º passo do login).
func (s *Server) handleMFAEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Challenge string `json:"challenge"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	uid, ok := s.challengeUser(r.Context(), body.Challenge)
	if !ok {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CHALLENGE", "Desafio inválido ou expirado"))
		return
	}
	u, err := s.userByID(r.Context(), uid)
	if err != nil || u == nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CHALLENGE", "Desafio inválido"))
		return
	}
	s.sendChallengeEmailCode(r.Context(), body.Challenge, u.Email)
	writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}

// POST /auth/mfa/verify {challenge, code} — valida TOTP/email/recovery e emite a sessão.
func (s *Server) handleMFAVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	uid, ok := s.challengeUser(r.Context(), body.Challenge)
	if !ok {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CHALLENGE", "Desafio inválido ou expirado"))
		return
	}
	u, err := s.userByID(r.Context(), uid)
	if err != nil || u == nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CHALLENGE", "Desafio inválido"))
		return
	}
	if u.LoginDisabled {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CHALLENGE", "Desafio inválido"))
		return
	}
	// Teto de tentativas por challenge: sem isso, o OTP de email (6 dígitos) seria
	// retentável até o limite por IP (burlável). Após N falhas, invalida o desafio.
	attempts, _ := s.rdb.Incr(r.Context(), "mfa_attempts:"+body.Challenge).Result()
	if attempts == 1 {
		s.rdb.Expire(r.Context(), "mfa_attempts:"+body.Challenge, 10*time.Minute)
	}
	if attempts > 5 {
		s.rdb.Del(r.Context(), "mfa_challenge:"+body.Challenge, "mfa_email:"+body.Challenge, "mfa_attempts:"+body.Challenge)
		writeErr(w, appErr(http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS", "Muitas tentativas. Faça login novamente."))
		return
	}
	code := strings.TrimSpace(body.Code)
	valid := false
	if u.TOTPSecret != nil && totp.Validate(code, *u.TOTPSecret) {
		valid = true
	}
	if !valid {
		if ec, e := s.rdb.Get(r.Context(), "mfa_email:"+body.Challenge).Result(); e == nil && ec != "" &&
			subtle.ConstantTimeCompare([]byte(ec), []byte(code)) == 1 {
			valid = true
			s.rdb.Del(r.Context(), "mfa_email:"+body.Challenge)
		}
	}
	if !valid && s.consumeRecoveryCode(r.Context(), uid, sha256Hex(strings.ToUpper(code))) {
		valid = true
	}
	if !valid {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código inválido"))
		return
	}
	s.rdb.Del(r.Context(), "mfa_challenge:"+body.Challenge, "mfa_attempts:"+body.Challenge)
	if err := s.issueSession(r.Context(), w, r, u); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": s.buildProfile(r.Context(), u)})
}

func (s *Server) challengeUser(ctx context.Context, challenge string) (int64, bool) {
	if challenge == "" {
		return 0, false
	}
	idStr, err := s.rdb.Get(ctx, "mfa_challenge:"+challenge).Result()
	if err != nil || idStr == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	return id, err == nil
}

func genRecoveryCodes(n int) []string {
	codes := make([]string, n)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	for i := range codes {
		b := make([]byte, 5)
		_, _ = rand.Read(b)
		codes[i] = enc.EncodeToString(b) // 8 chars A-Z2-7
	}
	return codes
}

func emailCode() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	n := int(b[0])<<16 | int(b[1])<<8 | int(b[2])
	return fmt.Sprintf("%06d", n%1000000)
}
