package main

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pquerna/otp/totp"
)

// Sudo mode (estilo GitHub): ações sensíveis exigem re-confirmação recente da
// identidade. O /auth/sudo/verify valida um fator (MFA ou senha, quando sem MFA)
// e re-emite o access_token com o claim sudo_exp; qualquer API do ecossistema
// valida a elevação offline, lendo o claim do mesmo JWT que já verifica.
const sudoTTL = 15 * time.Minute

// generateSudoAccess re-emite um access token com o claim sudo_exp.
func generateSudoAccess(secret string, userID int64, email string, sudoExp time.Time) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":      strconv.FormatInt(userID, 10),
		"iat":      now.Unix(),
		"exp":      now.Add(accessTTL).Unix(),
		"sudo_exp": sudoExp.Unix(),
	}
	if email != "" {
		claims["email"] = email
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// tokenSudoUntil extrai o sudo_exp de um access token já confiável (zero se ausente).
func tokenSudoUntil(token, secret string) time.Time {
	t, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(secret), nil
	})
	if err != nil || !t.Valid {
		return time.Time{}
	}
	claims, ok := t.Claims.(jwt.MapClaims)
	if !ok {
		return time.Time{}
	}
	exp, ok := claims["sudo_exp"].(float64)
	if !ok {
		return time.Time{}
	}
	return time.Unix(int64(exp), 0)
}

// sudoGuard exige elevação recente (sudo mode) além da sessão. Use por cima de
// authGuard/adminGuard nas rotas perigosas. Sem elevação → 403 SUDO_REQUIRED,
// e o front redireciona pro /confirm do auth-web.
func (s *Server) sudoGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if c, err := r.Cookie("access_token"); err == nil {
			token = c.Value
		}
		if token == "" {
			if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
				token = after
			}
		}
		if tokenSudoUntil(token, s.cfg.JWTSecret).Before(time.Now()) {
			writeErr(w, appErr(http.StatusForbidden, "SUDO_REQUIRED", "Confirme sua identidade para esta ação"))
			return
		}
		next(w, r)
	}
}

// POST /auth/sudo/verify {code?, password?} — confirma a identidade e eleva a
// sessão por sudoTTL. Com MFA ativo aceita TOTP/recovery/código por email
// (/auth/mfa/email-code); sem MFA, aceita a senha da conta.
func (s *Server) handleSudoVerify(w http.ResponseWriter, r *http.Request) {
	uid := userIDFrom(r)
	var body struct {
		Code     string `json:"code"`
		Password string `json:"password"`
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

	// Teto de tentativas por usuário (≤5 numa janela), espelhando o handleMFAVerify:
	// a verificação de sudo checa TOTP/recovery/OTP de email (ou senha) sem teto
	// próprio, o que permitiria brute-force do 2º fator limitado só pelo IP. Conta
	// a tentativa ANTES de validar; zera ao confirmar com sucesso (mais abaixo).
	attemptKey := "sudo_attempts:" + strconv.FormatInt(uid, 10)
	sudoAttemptsCmd := s.rdb.Incr(r.Context(), attemptKey)
	if sudoAttemptsCmd.Err() != nil {
		slog.Warn("sudo_attempts: redis error; rejecting to fail closed", "uid", uid, "err", sudoAttemptsCmd.Err())
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro interno. Tente novamente."))
		return
	}
	attempts := sudoAttemptsCmd.Val()
	if err := s.rdb.ExpireNX(r.Context(), attemptKey, 15*time.Minute).Err(); err != nil {
		slog.Warn("sudo_attempts: ExpireNX falhou; contador pode não expirar", "uid", uid, "err", err)
	}
	if attempts > 5 {
		writeErr(w, appErr(http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS", "Muitas tentativas. Tente novamente mais tarde."))
		return
	}

	valid := false
	if u.MFAEnabled {
		code := strings.TrimSpace(body.Code)
		if code != "" {
			if u.TOTPSecret != nil && totp.Validate(code, *u.TOTPSecret) {
				valid = true
			}
			if !valid {
				valid = s.consumeAcctEmailCode(r.Context(), uid, code)
			}
			if !valid {
				valid = s.consumeRecoveryCode(r.Context(), uid, sha256Hex(strings.ToUpper(code)))
			}
		}
	} else if body.Password != "" && u.PasswordHash != nil {
		valid = verifyPassword(body.Password, *u.PasswordHash)
	}
	if !valid {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CODE", "Confirmação inválida"))
		return
	}
	s.rdb.Del(r.Context(), attemptKey) // fator válido → zera o contador

	sudoExp := time.Now().Add(sudoTTL)
	access, err := generateSudoAccess(s.cfg.JWTSecret, u.ID, u.Email, sudoExp)
	if err != nil {
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL", "Erro ao elevar a sessão"))
		return
	}
	// Só o access é re-emitido; o refresh (e a sessão persistida) ficam como estão.
	s.setCookie(w, "access_token", access, int(accessTTL.Seconds()))
	writeJSON(w, http.StatusOK, map[string]any{
		"sudo":      true,
		"sudoUntil": sudoExp.UTC().Format(time.RFC3339),
	})
}
