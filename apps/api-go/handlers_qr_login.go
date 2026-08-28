package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const qrLoginTTL = 5 * time.Minute

// qrLoginRecord é o que fica gravado no Redis sob a chave do token — o código
// curto é só um PONTEIRO pro token (qrLoginCodeKey), pra resolver as duas
// formas de referenciar o mesmo pedido de login.
//
// Fluxo (igual ao pareamento por QR do hour-timer-app, só que pra sessão de
// usuário em vez de sessão de cliente): o SANTOS HUB gera o pedido (sem
// sessão nenhuma — é ele quem PRECISA logar) e mostra o QR/código na tela;
// alguém com uma sessão de verdade (celular ou outro PC já logado) escaneia
// com a CÂMERA DO CELULAR ou digita o código em
// dashboard/web:/conectar-dispositivo e confirma; o Hub só fica dando poll
// até aparecer aprovado.
type qrLoginRecord struct {
	Token    string `json:"token"`
	Code     string `json:"code"`
	Approved bool   `json:"approved"`
	UserID   int64  `json:"userID,omitempty"`
}

func qrLoginTokenKey(token string) string { return "qr_login:token:" + token }
func qrLoginCodeKey(code string) string   { return "qr_login:code:" + code }

func (s *Server) saveQRLoginRecord(w http.ResponseWriter, r *http.Request, rec qrLoginRecord) bool {
	data, err := json.Marshal(rec)
	if err != nil {
		writeErr(w, err)
		return false
	}
	if err := s.rdb.Set(r.Context(), qrLoginTokenKey(rec.Token), data, qrLoginTTL).Err(); err != nil {
		writeErr(w, err)
		return false
	}
	return true
}

// POST /public/qr-login/create — o Santos Hub (sem sessão) chama isso ao
// abrir "Outras opções" e mostra o QR (payload = token) + o código de 6
// dígitos como alternativa digitável. Público: quem cria não está logado.
func (s *Server) handleQRLoginCreate(w http.ResponseWriter, r *http.Request) {
	// resposta carrega token+code — equivalem a uma credencial de login, não
	// podem ser cacheados (ver mesmo padrão em handlers_auth/mfa/oauth_provider).
	w.Header().Set("Cache-Control", "no-store")
	token := randomToken(32)
	code := randomDigits(6)
	if err := s.rdb.Set(r.Context(), qrLoginCodeKey(code), token, qrLoginTTL).Err(); err != nil {
		writeErr(w, err)
		return
	}
	if !s.saveQRLoginRecord(w, r, qrLoginRecord{Token: token, Code: code}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     token,
		"code":      code,
		"expiresAt": time.Now().Add(qrLoginTTL).UTC().Format(time.RFC3339),
	})
}

// resolveQRLoginToken aceita tanto o token cru (payload do QR) quanto o
// código de 6 dígitos (digitado) e devolve o token canônico.
func (s *Server) resolveQRLoginToken(r *http.Request, value string) (string, error) {
	if len(value) == 64 {
		return value, nil
	}
	token, err := s.rdb.Get(r.Context(), qrLoginCodeKey(value)).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return token, err
}

// POST /auth/qr-login/confirm {code} — chamado pela sessão que JÁ está
// logada (o celular que escaneou, ou outra aba já autenticada) pra autorizar
// o Santos Hub a entrar. `code` aceita o token (64 chars, vindo do QR) ou o
// código de 6 dígitos digitado. Requer sessão — é ela quem empresta a
// identidade pro dispositivo novo.
func (s *Server) handleQRLoginConfirm(w http.ResponseWriter, r *http.Request) {
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
	token, err := s.resolveQRLoginToken(r, value)
	if err != nil {
		writeErr(w, err)
		return
	}
	if token == "" {
		writeErr(w, appErr(http.StatusNotFound, "QR_LOGIN_NOT_FOUND", "código expirado ou inválido"))
		return
	}
	data, err := s.rdb.Get(r.Context(), qrLoginTokenKey(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		writeErr(w, appErr(http.StatusNotFound, "QR_LOGIN_NOT_FOUND", "código expirado ou inválido"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	var rec qrLoginRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		writeErr(w, err)
		return
	}
	rec.Approved = true
	rec.UserID = int64(userIDFrom(r))
	if !s.saveQRLoginRecord(w, r, rec) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GET /public/qr-login/poll?token=... — o Santos Hub fica chamando isso a
// cada ~2s (o token nunca é digitado por gente, então é seguro usá-lo direto
// aqui, sem passar pelo resolve de código). Público. Uso único: assim que
// devolve a sessão, apaga o registro.
func (s *Server) handleQRLoginPoll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if len(token) != 64 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "token inválido"))
		return
	}
	data, err := s.rdb.Get(r.Context(), qrLoginTokenKey(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		writeJSON(w, http.StatusOK, map[string]any{"ready": false})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	var rec qrLoginRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		writeErr(w, err)
		return
	}
	if !rec.Approved {
		writeJSON(w, http.StatusOK, map[string]any{"ready": false})
		return
	}

	// Uso único: apaga token + código antes de emitir a sessão.
	if err := s.rdb.Del(r.Context(), qrLoginTokenKey(rec.Token), qrLoginCodeKey(rec.Code)).Err(); err != nil {
		writeErr(w, err)
		return
	}
	u, err := s.userByID(r.Context(), rec.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil || u.LoginDisabled || u.SuspendedAt != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ready": false})
		return
	}
	access, refresh, err := s.issueSession(r.Context(), w, r, u)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ready":        true,
		"user":         s.buildProfile(r.Context(), u),
		"accessToken":  access,
		"refreshToken": refresh,
	})
}
