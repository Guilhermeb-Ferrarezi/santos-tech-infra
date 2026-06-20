package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
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
	s.enqueueEmail("sendChallengeEmailCode", email, "Seu código de verificação — Santos Tech", html)
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
	// SetNX é atômico: exatamente uma requisição concorrente "adquire" o slot de
	// envio. O par Exists + Set não-atômico deixava uma janela onde duas chamadas
	// simultâneas ambas passavam pelo check e enviavam dois OTPs, sobrescrevendo o
	// código no Redis (segunda requisiçao sempre ganhava; OTP da primeira era inválido).
	acquired, errCD := s.rdb.SetNX(r.Context(), mfaEmailAcctCDKey(uid), "1", mfaEmailAcctCooldown).Result()
	if errCD != nil {
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL", "Erro ao verificar cooldown"))
		return
	}
	if !acquired {
		writeErr(w, appErr(http.StatusTooManyRequests, "RATE_LIMITED", "Aguarde um pouco antes de reenviar"))
		return
	}
	code := emailCode()
	if err := s.rdb.Set(r.Context(), mfaEmailAcctKey(uid), code, mfaEmailAcctTTL).Err(); err != nil {
		// Desfaz o cooldown para que o usuário possa retentar imediatamente.
		_ = s.rdb.Del(r.Context(), mfaEmailAcctCDKey(uid))
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL", "Erro ao gerar o código"))
		return
	}
	html := emailCodeHTML(code, "Use o código abaixo para confirmar a alteração do 2FA da sua conta:")
	s.enqueueEmail("handleMFAEmailCode", u.Email, "Seu código de verificação — Santos Tech", html)
	writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}

// consumeAcctEmailCode valida (tempo constante) e consome o código enviado pro email
// da conta. One-shot: válido uma única vez.
func (s *Server) consumeAcctEmailCode(ctx context.Context, uid int64, code string) bool {
	// GetDel é atômico: garante que duas chamadas concorrentes não consigam
	// consumir o mesmo código (TOCTOU entre Get + Del separados).
	want, err := s.rdb.GetDel(ctx, mfaEmailAcctKey(uid)).Result()
	if err != nil || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(code)), []byte(want)) == 1
}

// POST /auth/mfa/email-enable {code} — ativa o 2FA por email: exige email verificado
// e o código recém-enviado (/auth/mfa/email-code). Se for o primeiro método, vira o
// preferido; gera códigos de recuperação na primeira ativação do MFA.
func (s *Server) handleMFAEmailEnable(w http.ResponseWriter, r *http.Request) {
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
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código inválido"))
		return
	}
	// Teto de tentativas por usuário (≤5 numa janela de 15min): sem isso, o OTP de
	// e-mail (6 dígitos, 10⁶ combinações) seria brute-forçável limitado só pelo
	// rate-limit por IP — bypassável com múltiplos IPs/VPNs. Conta a tentativa ANTES
	// de validar; zera ao ativar com sucesso (mais abaixo). fail-closed: Redis
	// indisponível → rejeitar é mais seguro que deixar passar.
	attemptKey := "api-go:mfa_email_enable_attempts:" + strconv.FormatInt(uid, 10)
	attemptsCmd := s.rdb.Incr(r.Context(), attemptKey)
	if attemptsCmd.Err() != nil {
		slog.Warn("mfa_email_enable_attempts: redis error; rejecting to fail closed", "uid", uid, "err", attemptsCmd.Err())
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro interno. Tente novamente."))
		return
	}
	if err := s.rdb.ExpireNX(r.Context(), attemptKey, 15*time.Minute).Err(); err != nil {
		slog.Warn("mfa_email_enable_attempts: ExpireNX falhou; contador pode não expirar", "uid", uid, "err", err)
	}
	if attemptsCmd.Val() > 5 {
		writeErr(w, appErr(http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS", "Muitas tentativas. Tente novamente mais tarde."))
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
	if !s.consumeAcctEmailCode(r.Context(), uid, code) {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código inválido ou expirado"))
		return
	}
	// Fator válido → zera o contador de tentativas
	if err := s.rdb.Del(r.Context(), attemptKey).Err(); err != nil {
		slog.Warn("mfa_email_enable: falha ao remover contador de tentativas do Redis", "uid", uid, "err", err)
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
	s.invalidateUserCache(uid)
	// Recovery codes só na primeira ativação do MFA (não regenera ao somar método).
	resp := map[string]any{"enabled": true, "method": method}
	if !u.MFAEnabled {
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
		resp["recoveryCodes"] = codes
	}
	writeJSON(w, http.StatusOK, resp)
}

// POST /auth/mfa/method {method} — define o método preferido do 2º passo ('totp' ou
// 'email'). Só aceita método de fato disponível na conta.
func (s *Server) handleMFAMethod(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
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
	s.invalidateUserCache(uid)
	writeJSON(w, http.StatusOK, map[string]string{"method": body.Method})
}
