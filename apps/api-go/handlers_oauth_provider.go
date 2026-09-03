package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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
	// Rejeita requestId de formato inválido antes de qualquer I/O (Redis) — espelha
	// isValidChallenge (MFA) e isValidResetToken (reset de senha): IDs bem-formados
	// têm exatamente 32 chars hex (output de randomToken(16)); qualquer outro valor
	// nunca existirá no Redis e não precisa nem chegar lá.
	if !isValidOAuthRequestID(body.RequestID) {
		writeErr(w, appErr(http.StatusGone, "REQUEST_EXPIRED", "Autorização expirou — recomece a partir do app"))
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
	if u.SuspendedAt != nil || u.LoginDisabled {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta suspensa"))
		return
	}
	if !s.canAuthorizeOAuthClient(r.Context(), u, req.ClientID) {
		writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "Sem permissão para acessar este aplicativo"))
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
	// Consome o requestId só após sucesso. Logar falha do Del: se ficar no Redis,
	// abre janela de replay do requestId até expirar sozinho.
	if err := s.rdb.Del(r.Context(), authReqKey(body.RequestID)).Err(); err != nil {
		slog.Warn("oauth: falha ao consumir requestId do Redis (janela de replay)", "err", err)
	}

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
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
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
	redirectURI := r.PostFormValue("redirect_uri")
	verifier := r.PostFormValue("code_verifier")
	if code == "" || redirectURI == "" || verifier == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "code, redirect_uri e code_verifier são obrigatórios"))
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
	// client_id é opcional no body para clients públicos (PKCE — RFC 7636 §4.5).
	// Se fornecido, deve conferir com o code; se ausente, o code já identifica o client.
	if clientID := r.PostFormValue("client_id"); clientID != "" && ac.ClientID != clientID {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_GRANT", "Parâmetros não conferem com o code"))
		return
	}
	if ac.RedirectURI != redirectURI || !verifyPKCE(verifier, ac.CodeChallenge) {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_GRANT", "Parâmetros não conferem com o code"))
		return
	}
	u, err := s.userByID(r.Context(), ac.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil || u.SuspendedAt != nil || u.LoginDisabled {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta indisponível"))
		return
	}
	s.writeTokenResponse(w, r, u, ac.ClientID)
}

func (s *Server) oauthTokenRefresh(w http.ResponseWriter, r *http.Request) {
	refresh := r.PostFormValue("refresh_token")
	if refresh == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "refresh_token é obrigatório"))
		return
	}
	tokenUID, _, err := verifyToken(refresh, s.cfg.JWTRefreshSecret, tokenTypeRefresh)
	if err != nil {
		writeErr(w, appErr(http.StatusUnauthorized, "INVALID_GRANT", "refresh_token inválido"))
		return
	}
	// O refresh carrega o aud do client que o recebeu — sem isso a rotação
	// devolveria um token sem aud (= sessão do painel) e desfaria a marcação.
	// Vazio para refresh tokens emitidos antes desta mudança.
	clientID := tokenAudience(refresh, s.cfg.JWTRefreshSecret)
	sid, uid, expires, err := s.sessionByHash(r.Context(), hashRefreshToken(refresh))
	if errors.Is(err, pgx.ErrNoRows) {
		// Mesma detecção de reuso de handleRefresh (ver comentário lá): JWT válido
		// sem sessão correspondente é indício de token já rotacionado sendo
		// reusado. Usamos tokenUID (do JWT), não o uid de sessionByHash — este
		// último vem zerado quando a linha não existe. Só entra aqui em
		// ErrNoRows; erro de banco genérico cai no ramo seguinte, sem revogar nada.
		if delErr := s.deleteUserSessions(r.Context(), tokenUID); delErr != nil {
			slog.Error("oauth_token_refresh: falha ao revogar sessões após possível reuso", "uid", tokenUID, "err", delErr)
		} else {
			slog.Warn("oauth_token_refresh: refresh token sem sessão correspondente — sessões revogadas por precaução", "uid", tokenUID)
		}
		writeErr(w, appErr(http.StatusUnauthorized, "INVALID_GRANT", "Sessão expirada"))
		return
	}
	if err != nil {
		slog.Error("oauth_token_refresh: erro ao consultar sessão", "err", err)
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro ao renovar sessão"))
		return
	}
	if expires.Before(time.Now()) {
		writeErr(w, appErr(http.StatusUnauthorized, "INVALID_GRANT", "Sessão expirada"))
		return
	}
	u, err := s.userByID(r.Context(), uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil || u.SuspendedAt != nil || u.LoginDisabled {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta indisponível"))
		return
	}
	// fail-closed: não emitir token novo se não conseguir revogar o anterior
	// (evita dois refresh tokens simultâneos ativos para a mesma sessão).
	// Espelha o comportamento do handleRefresh para o fluxo cookie.
	if err := s.deleteSession(r.Context(), sid); err != nil {
		slog.Error("oauth_token_refresh: falha ao revogar sessão anterior", "sid", sid, "err", err)
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro ao renovar sessão"))
		return
	}
	s.writeTokenResponse(w, r, u, clientID)
}

// GET /oauth/userinfo — OIDC UserInfo endpoint (OpenID Connect Core §5.3).
// Recebe o Bearer access_token emitido em /oauth/token e devolve os claims
// do usuário no formato flat exigido pelo OIDC (sub, email, name).
func (s *Server) handleOAuthUserinfo(w http.ResponseWriter, r *http.Request) {
	token := ""
	if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		token = strings.TrimSpace(after)
	}
	if token == "" {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Bearer token ausente"))
		return
	}
	uid, u, err := s.resolveToken(r.Context(), token)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		u, err = s.userByID(r.Context(), uid)
		if err != nil || u == nil {
			writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Usuário não encontrado"))
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writePublicJSON(w, http.StatusOK, map[string]any{
		"sub":   strconv.FormatInt(u.ID, 10),
		"id":    u.ID,
		"email": u.Email,
		"name":  u.Name,
	})
}

// writeTokenResponse emite tokens + sessão e responde no formato OAuth.
// clientID marca o token com aud/scope (ver generateOAuthTokens em
// oauthprovider.go); vazio só acontece com refresh de token antigo, emitido
// antes desta mudança — nesse caso o par novo sai sem aud, como antes.
func (s *Server) writeTokenResponse(w http.ResponseWriter, r *http.Request, u *User, clientID string) {
	access, refresh, err := generateOAuthTokens(s.cfg.JWTSecret, s.cfg.JWTRefreshSecret, u.ID, u.Email, u.Name, clientID)
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
		"scope": oauthTokenScope,
	})
}

// canAuthorizeOAuthClient verifica se o usuário tem permissão para autorizar
// o client OAuth. Regras:
//   - Admin (role=3) → sempre permitido.
//   - Custom role (role=4) → permitido se permissions.oauth_clients contém o
//     clientID ou "*" (wildcard).
//   - Student / Teacher → negado por padrão.
func (s *Server) canAuthorizeOAuthClient(ctx context.Context, u *User, clientID string) bool {
	if u.Role == RoleAdmin {
		return true
	}
	if u.Role != RoleCustom || u.CustomRoleID == nil {
		return false
	}
	var raw []byte
	if err := s.db.QueryRow(ctx,
		`SELECT permissions FROM custom_roles WHERE id=$1`, *u.CustomRoleID,
	).Scan(&raw); err != nil || len(raw) == 0 {
		return false
	}
	var perms struct {
		OAuthClients []string `json:"oauth_clients"`
	}
	if err := json.Unmarshal(raw, &perms); err != nil {
		return false
	}
	for _, c := range perms.OAuthClients {
		if c == "*" || c == clientID {
			return true
		}
	}
	return false
}
