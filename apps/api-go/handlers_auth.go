package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// issueSession gera tokens, grava a sessão (refresh hash), seta os cookies e
// anexa a sessão ao cookie multi-conta "accounts". replaceSIDs: sessões antigas
// a tirar da lista (ex.: rotação de refresh).
func (s *Server) issueSession(ctx context.Context, w http.ResponseWriter, r *http.Request, u *User, replaceSIDs ...string) error {
	access, refresh, err := generateTokens(s.cfg.JWTSecret, s.cfg.JWTRefreshSecret, u.ID, u.Email)
	if err != nil {
		return err
	}
	sid, err := s.createSession(ctx, u.ID, hashRefreshToken(refresh), time.Now().Add(refreshTTL))
	if err != nil {
		return err
	}
	s.setAuthCookies(w, access, refresh)
	s.appendAccount(w, r, sid, replaceSIDs...)
	return nil
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
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
	body.Name = strings.TrimSpace(body.Name)
	if body.Email == "" || body.Name == "" || len(body.Name) > 128 || len(body.Password) < 8 || len(body.Password) > 128 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "email, nome e senha (entre 8 e 128 caracteres) são obrigatórios"))
		return
	}
	if !emailRe.MatchString(body.Email) {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "email inválido"))
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

// Lockout de login por conta (complementa o limite por IP): trava após N falhas
// numa janela, mitigando credential-stuffing distribuído contra uma única conta.
const (
	maxLoginFails   = 10
	loginFailWindow = 15 * time.Minute
)

// dummyPasswordHash é computado uma vez no boot para que handleLogin sempre
// execute verifyPassword (argon2id, ~300ms) mesmo quando o identificador não
// corresponde a nenhuma conta — impede enumeração de usuários por timing.
var dummyPasswordHash = func() string {
	h, err := hashPassword("__timing_sentinel__")
	if err != nil {
		panic("argon2id dummy hash: " + err.Error())
	}
	return h
}()

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		Identifier string `json:"identifier"`
		Password   string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	ident := strings.TrimSpace(body.Identifier)
	// Lockout chaveado por (IP + identifier), NÃO só pelo identifier: senão
	// qualquer um trancaria a vítima sabendo apenas o email (DoS de conta).
	// Com o IP na chave, o atacante só "tranca" a si mesmo contra aquela conta;
	// a proteção contra credential-stuffing distribuído continua vindo do
	// rate-limit por IP da rota (routes.go) + do limite global.
	lockKey := "login_fail:" + clientIP(r) + ":" + strings.ToLower(ident)
	if n, _ := s.rdb.Get(r.Context(), lockKey).Int(); n >= maxLoginFails {
		writeErr(w, appErr(http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS", "Muitas tentativas. Tente novamente mais tarde."))
		return
	}
	u, err := s.userByIdentifier(r.Context(), ident)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Normaliza o tempo de resposta: executa verifyPassword mesmo quando o
	// identificador não existe, tornando "conta inexistente" indistinguível
	// de "senha incorreta" para quem mede latências.
	hash := dummyPasswordHash
	if u != nil && u.PasswordHash != nil {
		hash = *u.PasswordHash
	}
	if u == nil || u.LoginDisabled || u.PasswordHash == nil || !verifyPassword(body.Password, hash) {
		s.rdb.Incr(r.Context(), lockKey)
		if err := s.rdb.ExpireNX(r.Context(), lockKey, loginFailWindow).Err(); err != nil {
			slog.Warn("login_fail: ExpireNX falhou; contador de lockout pode não expirar", "key", lockKey, "err", err)
		}
		writeErr(w, appErr(http.StatusUnauthorized, "INVALID_CREDENTIALS", "Email ou senha inválidos"))
		return
	}
	s.rdb.Del(r.Context(), lockKey) // credenciais válidas → zera o contador
	if u.SuspendedAt != nil {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta suspensa"))
		return
	}

	// MFA ativo: não emite tokens; devolve desafio pro 2º passo (/auth/mfa/verify).
	// Informa o método preferido (e os disponíveis) pra tela do código; quando o
	// preferido é email, o código já sai enviado (estilo GitHub).
	if u.MFAEnabled {
		challenge := randomToken(24)
		if err := s.rdb.Set(r.Context(), "mfa_challenge:"+challenge, u.ID, 10*time.Minute).Err(); err != nil {
			writeErr(w, err)
			return
		}
		if u.MFAMethod == "email" {
			if err := s.sendChallengeEmailCode(r.Context(), challenge, u.Email); err != nil {
				writeErr(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"mfaRequired": true, "challenge": challenge,
			"method": u.MFAMethod, "methods": mfaMethods(u),
		})
		return
	}

	if err := s.issueSession(r.Context(), w, r, u); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": s.buildProfile(r.Context(), u)})
}

// endSession apaga a sessão (pelo refresh cookie), limpa os cookies ativos e
// tira a conta do cookie multi-conta. As demais contas permanecem no chooser.
func (s *Server) endSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("refresh_token"); err == nil && c.Value != "" {
		if sid, _, _, e := s.sessionByHash(r.Context(), hashRefreshToken(c.Value)); e == nil {
			_ = s.deleteSession(r.Context(), sid)
			s.removeAccount(w, r, sid)
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

// allowedRedirect evita open-redirect: compara a ORIGEM exata (scheme://host),
// não apenas o prefixo — senão "https://mails.santos-tech.com.evil.com" passaria.
func (s *Server) allowedRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	origin := u.Scheme + "://" + u.Host
	for _, o := range s.cfg.CORSOrigins {
		if o != "" && origin == o {
			return true
		}
	}
	return s.cfg.AuthWebOrigin != "" && origin == s.cfg.AuthWebOrigin
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
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
	// resolveToken aceita JWT de sessão E PAT (st_...) — /auth/me precisa dos
	// dois, como toda rota atrás do authGuard.
	uid, err := s.resolveToken(r.Context(), token)
	if err != nil {
		writeErr(w, err)
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
	w.Header().Set("Cache-Control", "no-store")
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
	if u.SuspendedAt != nil || u.LoginDisabled {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado"))
		return
	}
	// fail-closed: não emitir token novo se não conseguir revogar o anterior (evita dois refresh tokens simultâneos)
	if err := s.deleteSession(r.Context(), sid); err != nil {
		slog.Error("refresh: falha ao revogar sessão anterior", "sid", sid, "err", err)
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro ao renovar sessão"))
		return
	}
	if err := s.issueSession(r.Context(), w, r, u, sid); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
