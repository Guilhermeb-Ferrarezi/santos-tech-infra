package main

import (
	"encoding/json"
	"net/http"
)

// GET /auth/google — redireciona pro consentimento do Google.
func (s *Server) handleGoogleStart(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		writeErr(w, appErr(http.StatusInternalServerError, "OAUTH_DISABLED", "OAuth não configurado"))
		return
	}
	state := randomToken(16)
	http.SetCookie(w, &http.Cookie{
		Name: "oauth_state", Value: state, Path: "/",
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
	// valida state (CSRF)
	sc, err := r.Cookie("oauth_state")
	if err != nil || sc.Value == "" || sc.Value != q.Get("state") {
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
		fail("oauth_failed")
		return
	}
	var profile struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(res.Body).Decode(&profile); err != nil {
		fail("oauth_failed")
		return
	}

	u, err := s.userByEmail(r.Context(), profile.Email)
	if err != nil || u == nil {
		fail("account_not_found")
		return
	}
	_ = s.linkOAuth(r.Context(), u.ID, "google", profile.ID)

	if err := s.issueSession(r.Context(), w, u); err != nil {
		fail("oauth_failed")
		return
	}
	http.Redirect(w, r, origin, http.StatusFound)
}
