package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"
)

// checkAndMarkTOTPUsed atomically marks a TOTP code as used for uid and reports
// whether it was already used. SetNX succeeds only once per (uid, code) within
// the 90 s TTL, which covers pquerna/otp default Skew=1, Period=30s (3 windows).
// Fail-closed: callers must reject the request when err != nil.
func (s *Server) checkAndMarkTOTPUsed(ctx context.Context, uid int64, code string) (alreadyUsed bool, err error) {
	key := fmt.Sprintf("api-go:totp_used:%d:%s", uid, code)
	acquired, err := s.rdb.SetNX(ctx, key, "1", 90*time.Second).Result()
	if err != nil {
		return false, err
	}
	return !acquired, nil
}

// POST /auth/mfa/setup — gera um secret TOTP pendente + otpauth URL (QR).
func (s *Server) handleMFASetup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
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
	if err := s.rdb.Del(r.Context(), "api-go:mfa_enable_attempts:"+uidStr).Err(); err != nil {
		slog.Warn("mfa_setup: falha ao resetar contador de tentativas", "uid", uid, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": key.Secret(), "otpauthUrl": key.URL()})
}

// POST /auth/mfa/enable {code} — confirma o TOTP e ativa o MFA, devolve recovery codes.
func (s *Server) handleMFAEnable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
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
	attemptKey := "api-go:mfa_enable_attempts:" + uidStr
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
	// Código TOTP tem 6 dígitos: rejeita comprimento inválido antes de validar.
	if len(code) != 6 {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código inválido"))
		return
	}
	if !totp.Validate(code, secret) {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código inválido"))
		return
	}
	codes := genRecoveryCodes(10)
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = sha256Hex(c)
	}
	// Agrupa as três escritas em uma transação: sem ela, se deleteRecoveryCodes
	// for bem mas insertRecoveryCodes falhar, o usuário fica com mfa_enabled=true
	// e zero recovery codes — estado irrecuperável sem intervenção manual.
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	if _, err := tx.Exec(r.Context(),
		`UPDATE users SET mfa_enabled=$1, totp_secret=$2 WHERE id=$3`, true, &secret, uid); err != nil {
		writeErr(w, err)
		return
	}
	if _, err := tx.Exec(r.Context(),
		`DELETE FROM recovery_codes WHERE user_id=$1`, uid); err != nil {
		writeErr(w, err)
		return
	}
	if _, err := tx.Exec(r.Context(),
		`INSERT INTO recovery_codes (user_id, code_hash) SELECT $1, unnest($2::text[])`, uid, hashes); err != nil {
		writeErr(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeErr(w, err)
		return
	}
	s.invalidateUserCache(uid)
	if err := s.rdb.Del(r.Context(), "mfa_setup:"+uidStr, attemptKey).Err(); err != nil {
		slog.Warn("mfa_enable: falha ao remover chaves de setup do Redis", "uid", uid, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "recoveryCodes": codes})
}

// POST /auth/mfa/disable {code} — desativa (aceita TOTP, recovery ou código por email).
func (s *Server) handleMFADisable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
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
	if len(code) != 6 && len(code) != recoveryCodeLen {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código inválido"))
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
	attemptKey := "api-go:mfa_disable_attempts:" + strconv.FormatInt(uid, 10)
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
	var valid bool
	if u.TOTPSecret != nil && totp.Validate(code, *u.TOTPSecret) {
		alreadyUsed, rdErr := s.checkAndMarkTOTPUsed(r.Context(), uid, code)
		if rdErr != nil {
			slog.Warn("mfa_disable: redis error ao verificar replay TOTP; rejeitando (fail-closed)", "uid", uid, "err", rdErr)
			writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro interno. Tente novamente."))
			return
		}
		if alreadyUsed {
			// Código TOTP matematicamente válido mas já consumido nesta janela de 90s.
			// Retorna imediatamente para não acionar consumeAcctEmailCode logo abaixo:
			// aquela função usa GetDel e destruiria o OTP de email pendente mesmo sem
			// que nenhuma autenticação tivesse ocorrido.
			writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código inválido"))
			return
		}
		valid = true
	}
	if !valid && len(code) == recoveryCodeLen {
		valid = s.consumeRecoveryCode(r.Context(), uid, sha256Hex(strings.ToUpper(code)))
	}
	// consumeAcctEmailCode usa GetDel — consome o OTP de email atomicamente mesmo
	// quando o código não bate. Só chamamos para códigos de 6 dígitos (OTP/TOTP):
	// recovery codes (13 chars) nunca são OTPs de email, então chamar GetDel aqui
	// invalidaria silenciosamente qualquer OTP pendente sem o usuário ter tentado usá-lo.
	if !valid && len(code) != recoveryCodeLen {
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
	uid, ok, redisErr := s.challengeUser(r.Context(), body.Challenge)
	if redisErr != nil {
		slog.Error("mfa_email: redis error ao verificar challenge", "err", redisErr)
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro interno. Tente novamente."))
		return
	}
	if !ok {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CHALLENGE", "Desafio inválido ou expirado"))
		return
	}
	// Cooldown por challenge (SetNX, atômico): impede burst de emails enviados para
	// a mesma vítima quando o atacante detém um challenge válido e usa múltiplos IPs
	// para contornar o rate-limit por IP da rota. Mesmo padrão de handleEmailVerifySend
	// e handleMFAEmailCode. A chave expira junto com o challenge (10min); 60s de
	// cooldown é suficiente para evitar flooding enquanto ainda permite reenvio legítimo.
	cdKey := "api-go:mfa_email_resend_cd:" + body.Challenge
	acquired, errCD := s.rdb.SetNX(r.Context(), cdKey, "1", 60*time.Second).Result()
	if errCD != nil {
		slog.Warn("mfa_email: redis error ao verificar cooldown de reenvio", "err", errCD)
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro interno. Tente novamente."))
		return
	}
	if !acquired {
		writeErr(w, appErr(http.StatusTooManyRequests, "RATE_LIMITED", "Aguarde um pouco antes de reenviar"))
		return
	}
	u, err := s.userByID(r.Context(), uid)
	if err != nil || u == nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CHALLENGE", "Desafio inválido"))
		return
	}
	// O 2º fator é escolha da CONTA, não de quem está tentando entrar. Sem esta
	// checagem, uma conta protegida só por TOTP era rebaixada para OTP por email
	// só porque o atacante (de posse da senha) pediu — bastava ter o email
	// verificado em algum momento. Erro genérico de propósito: não revela quais
	// fatores a conta tem.
	if !slices.Contains(mfaMethods(u), "email") {
		slog.Warn("mfa_email: pedido de OTP por email numa conta sem esse fator",
			"uid", uid, "methods", mfaMethods(u), "ip", clientIP(r))
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CHALLENGE", "Desafio inválido ou expirado"))
		return
	}
	if err := s.sendChallengeEmailCode(r.Context(), body.Challenge, u.Email); err != nil {
		// Desfaz o cooldown para que o usuário possa retentar imediatamente.
		_ = s.rdb.Del(r.Context(), cdKey)
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
	code := strings.TrimSpace(body.Code)
	if len(code) != 6 && len(code) != recoveryCodeLen {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Código inválido"))
		return
	}
	uid, ok, redisErr := s.challengeUser(r.Context(), body.Challenge)
	if redisErr != nil {
		slog.Error("mfa_verify: redis error ao verificar challenge", "err", redisErr)
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro interno. Tente novamente."))
		return
	}
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
	if u.SuspendedAt != nil {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta suspensa"))
		return
	}
	// Teto de tentativas por challenge: sem isso, o OTP de email (6 dígitos) seria
	// retentável até o limite por IP (burlável). Após N falhas, invalida o desafio.
	// fail-closed: se o Redis falhar, não sabemos quantas tentativas já houve;
	// rejeitar é mais seguro do que deixar passar (OTP de e-mail tem apenas 10⁶ combinações).
	attemptsCmd := s.rdb.Incr(r.Context(), "api-go:mfa_attempts:"+body.Challenge)
	if attemptsCmd.Err() != nil {
		slog.Warn("mfa_attempts: redis error; rejecting to fail closed", "challenge", maskForLog(body.Challenge), "err", attemptsCmd.Err())
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro interno. Tente novamente."))
		return
	}
	attempts := attemptsCmd.Val()
	if err := s.rdb.ExpireNX(r.Context(), "api-go:mfa_attempts:"+body.Challenge, 10*time.Minute).Err(); err != nil {
		slog.Warn("mfa_attempts: ExpireNX falhou; contador pode não expirar", "challenge", maskForLog(body.Challenge), "err", err)
	}
	if attempts > 5 {
		if err := s.rdb.Del(r.Context(), "mfa_challenge:"+body.Challenge, "mfa_email:"+body.Challenge, "api-go:mfa_attempts:"+body.Challenge).Err(); err != nil {
			slog.Warn("mfa_verify: falha ao invalidar desafio após excesso de tentativas", "challenge", maskForLog(body.Challenge), "err", err)
		}
		writeErr(w, appErr(http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS", "Muitas tentativas. Faça login novamente."))
		return
	}
	valid := false
	totpReplay := false
	if u.TOTPSecret != nil && totp.Validate(code, *u.TOTPSecret) {
		alreadyUsed, rdErr := s.checkAndMarkTOTPUsed(r.Context(), uid, code)
		if rdErr != nil {
			slog.Warn("mfa_verify: redis error ao verificar replay TOTP; rejeitando (fail-closed)", "challenge", maskForLog(body.Challenge), "err", rdErr)
			writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro interno. Tente novamente."))
			return
		}
		if !alreadyUsed {
			valid = true
		} else {
			totpReplay = true
		}
	}
	// GetDel consome o OTP de email de forma destrutiva antes de comparar — não
	// chamar quando o TOTP era válido mas já usado: o OTP seria invalidado sem que
	// nenhuma autenticação tivesse ocorrido (códigos são valores distintos e nunca vão bater).
	if !valid && !totpReplay {
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
	if err := s.rdb.Del(r.Context(), "mfa_challenge:"+body.Challenge, "mfa_email:"+body.Challenge, "api-go:mfa_attempts:"+body.Challenge).Err(); err != nil {
		slog.Error("mfa_verify: falha ao invalidar desafio após autenticação bem-sucedida", "challenge", maskForLog(body.Challenge), "err", err)
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro ao finalizar autenticação. Tente novamente."))
		return
	}
	access, refresh, err := s.issueSession(r.Context(), w, r, u)
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := map[string]any{"user": s.buildProfile(r.Context(), u)}
	if isNativeClient(r) {
		resp["accessToken"] = access
		resp["refreshToken"] = refresh
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) challengeUser(ctx context.Context, challenge string) (int64, bool, error) {
	if challenge == "" {
		return 0, false, nil
	}
	idStr, err := s.rdb.Get(ctx, "mfa_challenge:"+challenge).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if idStr == "" {
		return 0, false, nil
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, false, nil
	}
	return id, true, nil
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
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return fmt.Sprintf("%06d", n.Int64())
}
