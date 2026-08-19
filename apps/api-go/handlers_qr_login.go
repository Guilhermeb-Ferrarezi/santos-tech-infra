package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const qrLoginTTL = 2 * time.Minute

// qrLoginPayload é o que fica gravado no Redis, sob DUAS chaves (token e code)
// apontando pro mesmo valor — permite resgatar tanto pelo token completo
// (payload do QR, lido pela câmera) quanto pelo código curto (digitado à mão),
// e ao consumir apaga as duas de uma vez (uso único, independente de qual
// caminho foi usado pra resgatar).
type qrLoginPayload struct {
	UserID int64  `json:"userID"`
	Token  string `json:"token"`
	Code   string `json:"code"`
}

func qrLoginTokenKey(token string) string { return "qr_login:token:" + token }
func qrLoginCodeKey(code string) string   { return "qr_login:code:" + code }

// POST /auth/qr-login/start — gera o par token (QR)/código (digitável), válido
// por 2min. Login por QR reusa a MESMA sessão web já autenticada de quem gera
// (é essa sessão que autoriza o novo dispositivo) — sem passo de aprovação
// separado, ao contrário do WhatsApp Web.
func (s *Server) handleQRLoginStart(w http.ResponseWriter, r *http.Request) {
	uid := userIDFrom(r)
	token := randomToken(32)
	code := randomDigits(6)
	payload := qrLoginPayload{UserID: int64(uid), Token: token, Code: code}
	data, err := json.Marshal(payload)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.rdb.Set(r.Context(), qrLoginTokenKey(token), data, qrLoginTTL).Err(); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.rdb.Set(r.Context(), qrLoginCodeKey(code), data, qrLoginTTL).Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     token,
		"code":      code,
		"expiresAt": time.Now().Add(qrLoginTTL).UTC().Format(time.RFC3339),
	})
}

// POST /public/qr-login/exchange {code} — troca o token (QR) ou código (digitado)
// por uma sessão de verdade. Público: o próprio valor é a credencial (uso único,
// vida curta) — é assim que um dispositivo sem sessão nenhuma (o Hub, recém-aberto)
// consegue logar.
func (s *Server) handleQRLoginExchange(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	value := strings.TrimSpace(body.Code)
	if value == "" || len(value) > 128 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "código inválido"))
		return
	}

	// O token (hex, 64 chars) e o código curto (6 dígitos) usam namespaces
	// diferentes no Redis — tenta os dois, sem precisar o cliente informar qual é.
	data, err := s.rdb.Get(r.Context(), qrLoginTokenKey(value)).Bytes()
	if errors.Is(err, redis.Nil) {
		data, err = s.rdb.Get(r.Context(), qrLoginCodeKey(value)).Bytes()
	}
	if errors.Is(err, redis.Nil) {
		writeErr(w, appErr(http.StatusUnauthorized, "QR_LOGIN_INVALID", "código expirado ou já usado"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	var payload qrLoginPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		writeErr(w, err)
		return
	}
	// Uso único: apaga as duas chaves (token + code) já na troca, antes de
	// qualquer outra coisa — uma segunda tentativa concorrente com o mesmo
	// valor não encontra mais a chave e cai no ramo QR_LOGIN_INVALID acima.
	if err := s.rdb.Del(r.Context(), qrLoginTokenKey(payload.Token), qrLoginCodeKey(payload.Code)).Err(); err != nil {
		writeErr(w, err)
		return
	}

	u, err := s.userByID(r.Context(), payload.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil || u.LoginDisabled || u.SuspendedAt != nil {
		writeErr(w, appErr(http.StatusUnauthorized, "QR_LOGIN_INVALID", "código expirado ou já usado"))
		return
	}

	access, refresh, err := s.issueSession(r.Context(), w, r, u)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":         s.buildProfile(r.Context(), u),
		"accessToken":  access,
		"refreshToken": refresh,
	})
}
