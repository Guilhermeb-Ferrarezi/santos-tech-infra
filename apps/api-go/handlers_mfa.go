package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"log/slog"
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
	uidStr := strconv.FormatInt(uid, 10)
	if err := s.rdb.Set(r.Context(), "mfa_setup:"+uidStr, key.Secret(), 10*time.Minute).Err(); err != nil {
		writeErr(w, err)
		return
	}
	// Novo setup → zera o contador de tentativas para não bloquear o usuário que
	// regenerou o QR code após exceder o limite em handleMFAEnable.
	if err := s.rdb.Del(r.Context(), "mfa_enable_attempts:"+uidStr).Err(); err != nil {
		slog.Warn("mfa_setup: falha ao resetar contador de tentativas", "uid", uid, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": key.Secret(), "otpauthUrl": key.URL()})
}

// POST /auth/mfa/enable {code} — confirma o TOTP e ativa o MFA, devolve recovery codes.
func (s *Server) handleMFAEnable(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	uid := userIDFrom(r)
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	uidStr := strconv.FormatInt(uid, 10)
	secret, err := s.rdb.Get(r.Context(), "mfa_setup:"+uidStr).Result()
	if err != nil || secret == "" {
		writeErr(w, appErr(http.StatusBadRequest, "MFA_SETUP_EXPIRED", "Setup expirado, gere novamente"))
		return
	}
	// Teto de tentativas por usuário (≤5 numa janela de 15min), espelhando handleMFADisable:
	// sem isso, confirmar o setup do MFA fica sujeito a brute-force do código TOTP
	// limitado apenas pelo rate-limit por IP (bypassável com múltiplos IPs/VPNs).
	// fail-closed: se o Redis falhar, não sabemos quantas tentativas houve; rejeitar
	// é mais seguro do que deixar passar e permite ao usuário tentar novamente em instantes.
	attemptKey := "mfa_enable_attempts:" + uidStr
	enableAttemptsCmd := s.rdb.Incr(r.Context(), attemptKey)
	if enableAttemptsCmd.Err() != nil {
		slog.Warn("mfa_enable_attempts: redis error; rejecting to fail closed", "uid", uid, "err", enableAttemptsCmd.Err())
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro interno. Tente novamente."))
		return
	}
	attempts := enableAttemptsCmd.Val()
	if err := s.rdb.ExpireNX(r.Context(), attemptKey, 15*time.Minute).Err(); err != nil {
		slog.Warn("mfa_enable_attempts: ExpireNX falhou; contador pode não expirar", "uid", uid, "err", err)
	}
	if attempts > 5 {
		// Invalida o setup atual: força o usuário a gerar um novo QR code, o que
		// também zera o contador (em handleMFASetup).
		if err := s.rdb.Del(r.Context(), "mfa_setup:"+uidStr, attemptKey).Err(); err != nil {
			slog.Warn("mfa_enable: falha ao invalidar setup após excesso de tentativas", "uid", uid, "err", err)
		}
		writeErr(w, appErr(http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS", "Muitas tentativas. Gere um novo QR code."))
		return
	}
	code := strings.TrimSpace(body.Code)
	if !totp.Validate(code, secret) {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código inválido"))
		return
	}
	if err := s.setMFA(r.Context(), uid, true, &secret); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.rdb.Del(r.Context(), "mfa_setup:"+uidStr, attemptKey).Err(); err != nil {
		slog.Warn("mfa_enable: falha ao remover chaves de setup do Redis", "uid", uid, "err", err)
	}

	codes := genRecoveryCodes(10)
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = sha256Hex(c)
	}
	if err := s.deleteRecoveryCodes(r.Context(), uid); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.insertRecoveryCodes(r.Context(), uid, hashes); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "recoveryCodes": codes})
}

// POST /auth/mfa/disable {code} — desativa (aceita TOTP, recovery ou código por email).
func (s *Server) handleMFADisable(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
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
	// Teto de tentativas por usuário (≤5 numa janela), espelhando o handleMFAVerify:
	// sem isso, desabilitar o MFA viraria um ponto de brute-force do 2º fator
	// (TOTP/recovery/OTP de email) limitado só pelo rate-limit por IP. Conta a
	// tentativa ANTES de validar; zera ao desativar com sucesso (mais abaixo).
	attemptKey := "mfa_disable_attempts:" + strconv.FormatInt(uid, 10)
	disableAttemptsCmd := s.rdb.Incr(r.Context(), attemptKey)
	if disableAttemptsCmd.Err() != nil {
		slog.Warn("mfa_disable_attempts: redis error; rejecting to fail closed", "uid", uid, "err", disableAttemptsCmd.Err())
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro interno. Tente novamente."))
		return
	}
	attempts := disableAttemptsCmd.Val()
	if err := s.rdb.ExpireNX(r.Context(), attemptKey, 15*time.Minute).Err(); err != nil {
		slog.Warn("mfa_disable_attempts: ExpireNX falhou; contador pode não expirar", "uid", uid, "err", err)
	}
	if attempts > 5 {
		writeErr(w, appErr(http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS", "Muitas tentativas. Tente novamente mais tarde."))
		return
	}
	// Aceita qualquer fator válido da conta: TOTP, código de recuperação ou o código
	// enviado pro email (/auth/mfa/email-code) — necessário p/ contas só com email-2FA.
	code := strings.TrimSpace(body.Code)
	valid := u.TOTPSecret != nil && totp.Validate(code, *u.TOTPSecret)
	if !valid && len(code) == recoveryCodeLen {
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
	// fator válido → zera o contador de tentativas
	if err := s.rdb.Del(r.Context(), attemptKey).Err(); err != nil {
		slog.Warn("mfa_disable: falha ao remover contador de tentativas do Redis", "uid", uid, "err", err)
	}
	if err := s.deleteRecoveryCodes(r.Context(), uid); err != nil {
		slog.Warn("falha ao remover recovery codes ao desativar MFA", "uid", uid, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"disabled": true})
}

// POST /auth/mfa/email {challenge} — envia um código OTP por email (2º passo do login).
func (s *Server) handleMFAEmail(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		Challenge string `json:"challenge"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if !isValidChallenge(body.Challenge) {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CHALLENGE", "Desafio inválido ou expirado"))
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
	if err := s.sendChallengeEmailCode(r.Context(), body.Challenge, u.Email); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}

// POST /auth/mfa/verify {challenge, code} — valida TOTP/email/recovery e emite a sessão.
func (s *Server) handleMFAVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if !isValidChallenge(body.Challenge) {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CHALLENGE", "Desafio inválido ou expirado"))
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
	// fail-closed: se o Redis falhar, não sabemos quantas tentativas já houve;
	// rejeitar é mais seguro do que deixar passar (OTP de e-mail tem apenas 10⁶ combinações).
	attemptsCmd := s.rdb.Incr(r.Context(), "mfa_attempts:"+body.Challenge)
	if attemptsCmd.Err() != nil {
		slog.Warn("mfa_attempts: redis error; rejecting to fail closed", "challenge", body.Challenge, "err", attemptsCmd.Err())
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro interno. Tente novamente."))
		return
	}
	attempts := attemptsCmd.Val()
	if err := s.rdb.ExpireNX(r.Context(), "mfa_attempts:"+body.Challenge, 10*time.Minute).Err(); err != nil {
		slog.Warn("mfa_attempts: ExpireNX falhou; contador pode não expirar", "challenge", body.Challenge, "err", err)
	}
	if attempts > 5 {
		if err := s.rdb.Del(r.Context(), "mfa_challenge:"+body.Challenge, "mfa_email:"+body.Challenge, "mfa_attempts:"+body.Challenge).Err(); err != nil {
			slog.Warn("mfa_verify: falha ao invalidar desafio após excesso de tentativas", "challenge", body.Challenge, "err", err)
		}
		writeErr(w, appErr(http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS", "Muitas tentativas. Faça login novamente."))
		return
	}
	code := strings.TrimSpace(body.Code)
	valid := false
	if u.TOTPSecret != nil && totp.Validate(code, *u.TOTPSecret) {
		valid = true
	}
	if !valid {
		// GetDel é atômico: evita que duas requisições concorrentes com o mesmo
		// código ambas passem antes que a primeira remova a chave (TOCTOU).
		if ec, e := s.rdb.GetDel(r.Context(), "mfa_email:"+body.Challenge).Result(); e == nil && ec != "" &&
			subtle.ConstantTimeCompare([]byte(ec), []byte(code)) == 1 {
			valid = true
		}
	}
	if !valid && len(code) == recoveryCodeLen && s.consumeRecoveryCode(r.Context(), uid, sha256Hex(strings.ToUpper(code))) {
		valid = true
	}
	if !valid {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código inválido"))
		return
	}
	// fail-closed: se não conseguirmos invalidar o desafio, não emitimos a sessão.
	// Sem isso, uma falha transitória do Redis deixaria o challenge vivo por até
	// 10 min, permitindo replay com um novo código válido (TOTP ou OTP de email
	// que ainda não foi removido). Padrão consistente com o fail-closed do Incr
	// acima (linha ~216): Redis instável → rejeitar é mais seguro que deixar passar.
	if err := s.rdb.Del(r.Context(), "mfa_challenge:"+body.Challenge, "mfa_email:"+body.Challenge, "mfa_attempts:"+body.Challenge).Err(); err != nil {
		slog.Error("mfa_verify: falha ao invalidar desafio após autenticação bem-sucedida", "challenge", body.Challenge, "err", err)
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro ao finalizar autenticação. Tente novamente."))
		return
	}
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
		// 8 bytes => 64 bits de entropia (antes eram 5 bytes / 40 bits, fracos
		// para um código que dispensa o 2º fator). base32 sem padding → 13 chars.
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}
		codes[i] = enc.EncodeToString(b) // 13 chars A-Z2-7
	}
	return codes
}

func emailCode() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	n := int(b[0])<<16 | int(b[1])<<8 | int(b[2])
	return fmt.Sprintf("%06d", n%1000000)
}
