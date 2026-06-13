package main

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// oauthStateKey é a chave Redis onde guardamos o state do fluxo Google OAuth
// (lado servidor), validado e consumido no callback.
func oauthStateKey(state string) string { return "oauth_state:" + state }

// GET /auth/google?return_to=/caminho — redireciona pro consentimento do Google.
// return_to (opcional, só caminho relativo) é devolvido pelo callback — usado
// pelo fluxo OAuth provider pra voltar ao chooser com o request_id.
func (s *Server) handleGoogleStart(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		writeErr(w, appErr(http.StatusInternalServerError, "OAUTH_DISABLED", "OAuth não configurado"))
		return
	}
	state := randomToken(16)
	cookieVal := state
	// Só caminho relativo ("/x"), nunca "//host" — evita open-redirect.
	if rt := r.URL.Query().Get("return_to"); strings.HasPrefix(rt, "/") && !strings.HasPrefix(rt, "//") {
		cookieVal = state + "|" + rt
	}
	// Guarda o state no servidor (Redis, TTL curto) além do cookie. O callback
	// exige que o state exista aqui e o consome (one-time), então não confiamos
	// só num cookie que o cliente controla — fecha CSRF/replay no fluxo OAuth.
	if err := s.rdb.Set(r.Context(), oauthStateKey(state), "1", 10*time.Minute).Err(); err != nil {
		writeErr(w, appErr(http.StatusInternalServerError, "OAUTH_FAILED", "falha ao iniciar OAuth"))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "oauth_state", Value: cookieVal, Path: "/",
		HttpOnly: true, Secure: s.cfg.Production, SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
	http.Redirect(w, r, s.google.AuthCodeURL(state), http.StatusFound)
}

// GET /auth/google/callback — troca o code, busca o perfil, loga e redireciona.
func (s *Server) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	origin := s.cfg.AuthWebOrigin
	q := r.URL.Query()

	fail := func(reason string) {
		http.Redirect(w, r, origin+"/?error="+reason, http.StatusFound)
	}

	if q.Get("error") != "" {
		fail("oauth_denied")
		return
	}
	if s.google == nil {
		fail("oauth_failed")
		return
	}
	// valida state (CSRF) — o cookie pode carregar "|return_to" após o state
	sc, err := r.Cookie("oauth_state")
	if err != nil || sc.Value == "" {
		fail("oauth_failed")
		return
	}
	state, returnTo, _ := strings.Cut(sc.Value, "|")
	if state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(q.Get("state"))) != 1 {
		fail("oauth_failed")
		return
	}
	// Valida o state contra o que guardamos no servidor e o consome (one-time).
	// Del devolve 0 se a chave não existe (expirou ou já foi usada): nesses casos
	// recusamos, mesmo que o cookie "bata" — não confiamos só no cookie.
	if n, err := s.rdb.Del(r.Context(), oauthStateKey(state)).Result(); err != nil || n == 0 {
		fail("oauth_failed")
		return
	}
	code := q.Get("code")
	if code == "" {
		fail("oauth_failed")
		return
	}

	token, err := s.google.Exchange(r.Context(), code)
	if err != nil {
		fail("oauth_failed")
		return
	}

	res, err := s.google.Client(r.Context(), token).Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		fail("oauth_failed")
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, res.Body) // drain so the connection can be reused
		fail("oauth_failed")
		return
	}
	var profile struct {
		ID            string `json:"id"`
		Email         string `json:"email"`
		VerifiedEmail bool   `json:"verified_email"`
		Name          string `json:"name"`
	}
	if err := json.NewDecoder(res.Body).Decode(&profile); err != nil {
		fail("oauth_failed")
		return
	}
	// Só seguimos com email comprovadamente verificado pelo Google. Sem isso, uma
	// conta Google com email não-verificado poderia ser usada para "provar" um
	// endereço que não é dela e assumir a conta local correspondente.
	if !profile.VerifiedEmail {
		fail("account_not_found")
		return
	}

	u, err := s.userByEmail(r.Context(), profile.Email)
	if err != nil || u == nil {
		fail("account_not_found")
		return
	}
	if u.LoginDisabled {
		fail("account_not_found")
		return
	}
	// Auto-vincula a identidade Google à conta local apenas com email verificado
	// (já garantido acima) — evita vincular por email forjado.
	_ = s.linkOAuth(r.Context(), u.ID, "google", profile.ID)

	// MFA ativo: OAuth não pode contornar o 2º fator. Não emite sessão — cria o
	// mesmo desafio do login por senha e manda o auth-web pro passo do código.
	if u.MFAEnabled {
		challenge := randomToken(24)
		if err := s.rdb.Set(r.Context(), "mfa_challenge:"+challenge, u.ID, 10*time.Minute).Err(); err != nil {
			fail("oauth_failed")
			return
		}
		if u.MFAMethod == "email" {
			if err := s.sendChallengeEmailCode(r.Context(), challenge, u.Email); err != nil {
				fail("oauth_failed")
				return
			}
		}
		dest := origin + "/?mfa_challenge=" + challenge + "&mfa_method=" + u.MFAMethod +
			"&mfa_methods=" + strings.Join(mfaMethods(u), ",")
		if returnTo != "" {
			dest += "&return_to=" + url.QueryEscape(returnTo)
		}
		http.Redirect(w, r, dest, http.StatusFound)
		return
	}

	if err := s.issueSession(r.Context(), w, r, u); err != nil {
		fail("oauth_failed")
		return
	}
	dest := origin
	if returnTo != "" {
		dest = origin + returnTo
	}
	http.Redirect(w, r, dest, http.StatusFound)
}
