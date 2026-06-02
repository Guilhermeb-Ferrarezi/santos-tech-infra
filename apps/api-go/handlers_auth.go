package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// issueSession gera tokens, grava a sessão (refresh hash) e seta os cookies.
func (s *Server) issueSession(ctx context.Context, w http.ResponseWriter, u *User) error {
	access, refresh, err := generateTokens(s.cfg.JWTSecret, s.cfg.JWTRefreshSecret, u.ID, u.Email)
	if err != nil {
		return err
	}
	if err := s.createSession(ctx, u.ID, hashRefreshToken(refresh), time.Now().Add(refreshTTL)); err != nil {
		return err
	}
	s.setAuthCookies(w, access, refresh)
	return nil
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	body.Email = strings.TrimSpace(strings.ToLower(body.Email))
	if body.Email == "" || body.Name == "" || len(body.Password) < 8 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "email, nome e senha (mín. 8) são obrigatórios"))
		return
	}
	existing, err := s.userByEmail(r.Context(), body.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if existing != nil {
		writeErr(w, appErr(http.StatusConflict, "EMAIL_ALREADY_EXISTS", "Este email já está cadastrado"))
		return
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	u, err := s.insertUser(r.Context(), body.Email, body.Name, hash)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"user": map[string]any{
			"id": u.ID, "email": u.Email, "name": u.Name,
			"createdAt": u.CreatedAt.UTC().Format(time.RFC3339),
		},
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Identifier string `json:"identifier"`
		Password   string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	u, err := s.userByIdentifier(r.Context(), strings.TrimSpace(body.Identifier))
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil || u.PasswordHash == nil || !verifyPassword(body.Password, *u.PasswordHash) {
		writeErr(w, appErr(http.StatusUnauthorized, "INVALID_CREDENTIALS", "Email ou senha inválidos"))
		return
	}
	if u.SuspendedAt != nil {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta suspensa"))
		return
	}

	// MFA ativo: não emite tokens; devolve desafio pro 2º passo (/auth/mfa/verify)
	if u.MFAEnabled {
		challenge := randomToken(24)
		if err := s.rdb.Set(r.Context(), "mfa_challenge:"+challenge, u.ID, 10*time.Minute).Err(); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mfaRequired": true, "challenge": challenge})
		return
	}

	if err := s.issueSession(r.Context(), w, u); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": s.buildProfile(r.Context(), u)})
}

// endSession apaga a sessão (pelo refresh cookie) e limpa os cookies.
func (s *Server) endSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("refresh_token"); err == nil && c.Value != "" {
		if sid, _, _, e := s.sessionByHash(r.Context(), hashRefreshToken(c.Value)); e == nil {
			_ = s.deleteSession(r.Context(), sid)
		}
	}
	s.clearAuthCookies(w)
}

// POST /auth/logout — para clientes via XHR (204).
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.endSession(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// GET /auth/logout?redirect=... — desloga e redireciona (logout via link).
func (s *Server) handleLogoutGet(w http.ResponseWriter, r *http.Request) {
	s.endSession(w, r)
	dest := s.cfg.AuthWebOrigin
	if rd := r.URL.Query().Get("redirect"); rd != "" && s.allowedRedirect(rd) {
		dest = rd
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// allowedRedirect evita open-redirect: só permite origens conhecidas.
func (s *Server) allowedRedirect(url string) bool {
	for _, o := range s.cfg.CORSOrigins {
		if o != "" && strings.HasPrefix(url, o) {
			return true
		}
	}
	return s.cfg.AuthWebOrigin != "" && strings.HasPrefix(url, s.cfg.AuthWebOrigin)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	token := ""
	if c, err := r.Cookie("access_token"); err == nil {
		token = c.Value
	}
	if token == "" {
		if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			token = after
		}
	}
	if token == "" {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Não autenticado"))
		return
	}
	uid, err := verifyToken(token, s.cfg.JWTSecret)
	if err != nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado"))
		return
	}
	u, err := s.userByID(r.Context(), uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Usuário não encontrado"))
		return
	}
	if u.SuspendedAt != nil {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta suspensa"))
		return
	}
	s.rdb.Set(r.Context(), "user:last_seen:"+strconv.FormatInt(u.ID, 10), "1", 5*time.Minute)
	writeJSON(w, http.StatusOK, map[string]any{"user": s.buildProfile(r.Context(), u)})
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("refresh_token")
	if err != nil || c.Value == "" {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Refresh token ausente"))
		return
	}
	uid, err := verifyToken(c.Value, s.cfg.JWTRefreshSecret)
	if err != nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Refresh token inválido"))
		return
	}
	sid, _, expires, err := s.sessionByHash(r.Context(), hashRefreshToken(c.Value))
	if err != nil || expires.Before(time.Now()) {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Sessão expirada"))
		return
	}
	u, err := s.userByID(r.Context(), uid)
	if err != nil || u == nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Usuário não encontrado"))
		return
	}
	_ = s.deleteSession(r.Context(), sid) // rotaciona
	if err := s.issueSession(r.Context(), w, u); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
