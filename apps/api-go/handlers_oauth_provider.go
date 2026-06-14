package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"time"
)

// GET /oauth/authorize?client_id&redirect_uri&response_type=code&state
//
//	&code_challenge&code_challenge_method=S256
//
// Valida o client e manda o navegador pro chooser de contas do auth-web.
func (s *Server) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID, redirectURI := q.Get("client_id"), q.Get("redirect_uri")
	if clientID == "" || redirectURI == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "client_id e redirect_uri são obrigatórios"))
		return
	}
	client, err := s.oauthClientByClientID(r.Context(), clientID)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Client/redirect inválidos → erro JSON direto: NUNCA redirecionar pra URI
	// não confiável (regra da spec OAuth).
	if client == nil || !client.IsActive || !slices.Contains(client.RedirectURIs, redirectURI) {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_CLIENT", "Aplicação ou redirect_uri inválidos"))
		return
	}
	// Redirect validado: demais erros voltam pro app via ?error= (spec OAuth).
	redirectErr := func(code string) {
		u, _ := url.Parse(redirectURI)
		qq := u.Query()
		qq.Set("error", code)
		if st := q.Get("state"); st != "" {
			qq.Set("state", st)
		}
		u.RawQuery = qq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
	if q.Get("response_type") != "code" {
		redirectErr("unsupported_response_type")
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		redirectErr("invalid_request") // PKCE S256 é obrigatório
		return
	}

	id := randomToken(16)
	raw, _ := json.Marshal(authRequest{
		ClientID: clientID, ClientName: client.Name, RedirectURI: redirectURI,
		State: q.Get("state"), CodeChallenge: q.Get("code_challenge"),
	})
	if err := s.rdb.Set(r.Context(), authReqKey(id), raw, authReqTTL).Err(); err != nil {
		writeErr(w, err)
		return
	}
	http.Redirect(w, r, s.cfg.AuthWebOrigin+"/oauth/choose?request_id="+id, http.StatusFound)
}

// POST /oauth/authorize/confirm {requestId, sessionId} — o usuário escolheu a
// conta no chooser; emite o code e devolve a URL de retorno pro app.
// A requisição só é consumida APÓS sucesso: se a sessão escolhida morreu, o
// usuário volta ao chooser com o MESMO request_id (UX aprovada no spec).
func (s *Server) handleOAuthConfirm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		RequestID string `json:"requestId"`
		SessionID string `json:"sessionId"`
	}
	if err := decodeJSON(r, &body); err != nil || body.RequestID == "" || body.SessionID == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "requestId e sessionId são obrigatórios"))
		return
	}
	raw, err := s.rdb.Get(r.Context(), authReqKey(body.RequestID)).Bytes()
	if err != nil {
		writeErr(w, appErr(http.StatusGone, "REQUEST_EXPIRED", "Autorização expirou — recomece a partir do app"))
		return
	}
	var req authRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeErr(w, err)
		return
	}

	if !slices.Contains(s.readAccounts(r), body.SessionID) {
		writeErr(w, appErr(http.StatusUnauthorized, "SESSION_EXPIRED", "Sessão não encontrada neste navegador"))
		return
	}
	u, err := s.sessionUserByID(r.Context(), body.SessionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		s.removeAccount(w, r, body.SessionID) // auto-limpeza do cookie
		writeErr(w, appErr(http.StatusUnauthorized, "SESSION_EXPIRED", "Sessão expirada — entre novamente"))
		return
	}
	if u.SuspendedAt != nil {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta suspensa"))
		return
	}

	code := randomToken(32)
	rawCode, _ := json.Marshal(authCode{
		ClientID: req.ClientID, RedirectURI: req.RedirectURI,
		UserID: u.ID, CodeChallenge: req.CodeChallenge,
	})
	if err := s.rdb.Set(r.Context(), authCodeKey(code), rawCode, authCodeTTL).Err(); err != nil {
		writeErr(w, err)
		return
	}
	s.rdb.Del(r.Context(), authReqKey(body.RequestID)) // consome só após sucesso

	dest, _ := url.Parse(req.RedirectURI)
	qq := dest.Query()
	qq.Set("code", code)
	if req.State != "" {
		qq.Set("state", req.State)
	}
	dest.RawQuery = qq.Encode()
	w.Header().Set("Cache-Control", "no-store") // a URL contém o code de uso único
	writeJSON(w, http.StatusOK, map[string]string{"redirectTo": dest.String()})
}

// POST /oauth/token — application/x-www-form-urlencoded (padrão OAuth).
// Tokens voltam no CORPO (sem cookies): quem gerencia é o app cliente.
func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		s.oauthTokenCode(w, r)
	case "refresh_token":
		s.oauthTokenRefresh(w, r)
	default:
		writeErr(w, appErr(http.StatusBadRequest, "UNSUPPORTED_GRANT_TYPE", "grant_type deve ser authorization_code ou refresh_token"))
	}
}

func (s *Server) oauthTokenCode(w http.ResponseWriter, r *http.Request) {
	code := r.PostFormValue("code")
	clientID := r.PostFormValue("client_id")
	redirectURI := r.PostFormValue("redirect_uri")
	verifier := r.PostFormValue("code_verifier")
	if code == "" || clientID == "" || redirectURI == "" || verifier == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "code, client_id, redirect_uri e code_verifier são obrigatórios"))
		return
	}
	// GETDEL: o code é de uso único — a segunda troca falha sempre.
	raw, err := s.rdb.GetDel(r.Context(), authCodeKey(code)).Bytes()
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_GRANT", "code inválido ou expirado"))
		return
	}
	var ac authCode
	if err := json.Unmarshal(raw, &ac); err != nil {
		writeErr(w, err)
		return
	}
	if ac.ClientID != clientID || ac.RedirectURI != redirectURI || !verifyPKCE(verifier, ac.CodeChallenge) {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_GRANT", "Parâmetros não conferem com o code"))
		return
	}
	u, err := s.userByID(r.Context(), ac.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil || u.SuspendedAt != nil {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta indisponível"))
		return
	}
	s.writeTokenResponse(w, r, u)
}

func (s *Server) oauthTokenRefresh(w http.ResponseWriter, r *http.Request) {
	refresh := r.PostFormValue("refresh_token")
	if refresh == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "refresh_token é obrigatório"))
		return
	}
	if _, err := verifyToken(refresh, s.cfg.JWTRefreshSecret); err != nil {
		writeErr(w, appErr(http.StatusUnauthorized, "INVALID_GRANT", "refresh_token inválido"))
		return
	}
	sid, uid, expires, err := s.sessionByHash(r.Context(), hashRefreshToken(refresh))
	if err != nil || expires.Before(time.Now()) {
		writeErr(w, appErr(http.StatusUnauthorized, "INVALID_GRANT", "Sessão expirada"))
		return
	}
	u, err := s.userByID(r.Context(), uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil || u.SuspendedAt != nil {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta indisponível"))
		return
	}
	// Rotaciona criando uma sessão NOVA (id diferente): o fluxo OAuth não tem
	// cookies — o app cliente guarda os próprios tokens, então a sessão é
	// deliberadamente desacoplada do cookie multi-conta "accounts" do navegador.
	_ = s.deleteSession(r.Context(), sid)
	s.writeTokenResponse(w, r, u)
}

// writeTokenResponse emite tokens + sessão e responde no formato OAuth.
func (s *Server) writeTokenResponse(w http.ResponseWriter, r *http.Request, u *User) {
	access, refresh, err := generateTokens(s.cfg.JWTSecret, s.cfg.JWTRefreshSecret, u.ID, u.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := s.createSession(r.Context(), u.ID, hashRefreshToken(refresh), time.Now().Add(refreshTTL)); err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store") // resposta carrega credenciais (RFC 6749 §5.1)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "refresh_token": refresh,
		"token_type": "Bearer", "expires_in": int(accessTTL.Seconds()),
	})
}
