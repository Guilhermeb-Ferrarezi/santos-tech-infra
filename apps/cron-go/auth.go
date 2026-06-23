package main

import (
	"encoding/json"
	"net/http"
)

// requireAdmin repassa os cookies da request para a Auth API (/auth/me) e só
// segue se a resposta indicar papel Admin (role 3). Fail-closed.
//
// A resposta do /auth/me tem formato { "user": { "role": N, ... } } — o campo
// role fica aninhado em "user", conforme models.go (UserProfile) do api-go.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.cfg.AuthMeURL, nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "auth_error", "falha ao checar sessão")
			return
		}
		req.Header.Set("Cookie", r.Header.Get("Cookie"))
		if h := r.Header.Get("Authorization"); h != "" {
			req.Header.Set("Authorization", h)
		}
		resp, err := s.authClient.Do(req)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "sessão inválida")
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			writeError(w, http.StatusUnauthorized, "unauthorized", "sessão inválida")
			return
		}
		var me struct {
			User struct {
				Role int `json:"role"`
			} `json:"user"`
		}
		if json.NewDecoder(resp.Body).Decode(&me) != nil || me.User.Role != 3 {
			writeError(w, http.StatusForbidden, "forbidden", "requer papel Admin")
			return
		}
		next(w, r)
	}
}
