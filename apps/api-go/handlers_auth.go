package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

// issueSession gera tokens, grava a sessão (refresh hash), seta os cookies e
// anexa a sessão ao cookie multi-conta "accounts". replaceSIDs: sessões antigas
// a tirar da lista (ex.: rotação de refresh). Devolve os tokens gerados para que
// o caller decida se os expõe no corpo da resposta (ver isNativeClient).
func (s *Server) issueSession(ctx context.Context, w http.ResponseWriter, r *http.Request, u *User, replaceSIDs ...string) (access, refresh string, err error) {
	access, refresh, err = generateTokens(s.cfg.JWTSecret, s.cfg.JWTRefreshSecret, u.ID, u.Email, u.Name)
	if err != nil {
		return "", "", err
	}
	sid, err := s.createSession(ctx, u.ID, hashRefreshToken(refresh), time.Now().Add(refreshTTL))
	if err != nil {
		return "", "", err
	}
	s.setAuthCookies(w, access, refresh)
	s.appendAccount(w, r, sid, replaceSIDs...)
	return access, refresh, nil
}

// isNativeClient reconhece requisições sem o header Origin como vindas de um
// cliente não-browser (ex.: o app React Native, que não tem cookie jar). Navegadores
// SEMPRE mandam Origin em requests POST — mesmo same-origin, desde a spec de 2011 —
// e uma página com XSS não consegue forjar nem omitir esse header via fetch/XHR (é
// o próprio browser quem o define). Por isso é seguro devolver os tokens no corpo
// só quando Origin vem vazio: não abre uma via nova de vazamento pro fluxo web
// normal (que continua só-cookie), apenas atende quem já não tinha cookie nenhum.
func isNativeClient(r *http.Request) bool {
	return r.Header.Get("Origin") == ""
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
	if body.Email == "" || body.Name == "" || len(body.Email) > 254 || len(body.Name) > 128 || len(body.Password) < 8 || len(body.Password) > 128 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "email, nome e senha (entre 8 e 128 caracteres) são obrigatórios"))
		return
	}
	if !emailRe.MatchString(body.Email) {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "email inválido"))
		return
	}
	// Executa argon2id antes de consultar o banco para que o tempo de resposta
	// seja uniforme independentemente de o email já existir ou não — impede
	// enumeração de usuários por timing (mesma proteção do handleLogin).
	hash, err := hashPassword(body.Password)
	if err != nil {
		writeErr(w, err)
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
	u, err := s.insertUser(r.Context(), body.Email, body.Name, hash)
	if err != nil {
		// Violação de UNIQUE no banco: a checagem userByEmail + insertUser tem uma
		// janela TOCTOU onde dois requests concorrentes com o mesmo email passam pelo
		// check e ambos tentam inserir. O segundo recebe 23505 do Postgres; sem este
		// tratamento ele virava 500 em vez do 409 correto.
		if isUniqueViolation(err) {
			writeErr(w, appErr(http.StatusConflict, "EMAIL_ALREADY_EXISTS", "Este email já está cadastrado"))
			return
		}
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

// Lockout agregado SÓ por conta (sem o IP na chave), teto mais folgado que
// maxLoginFails: existe para o caso em que o IP do atacante é forjável (ver
// achado #2/#7 da auditoria de 2026-09 — clientIP() confia no peer do Traefik,
// que hoje aceita tráfego que não passou pela Cloudflare). Enquanto isso não
// for corrigido na borda, rotacionar CF-Connecting-IP abriria um balde novo do
// contador por (IP+conta) a cada tentativa; este contador não depende do IP,
// então continua travando o brute-force numa conta específica mesmo assim.
// Teto mais alto que o por-IP para não punir cedo demais o caso normal
// (várias contas erradas vindas de fato de IPs diferentes e legítimos, ex.:
// vários alunos no mesmo laboratório).
const (
	maxLoginFailsAccount   = 30
	loginFailAccountWindow = 15 * time.Minute
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
	if strings.Contains(ident, "@") {
		ident = strings.ToLower(ident)
	}
	// Rejeita entradas claramente inválidas antes de qualquer I/O (Redis/DB/argon2id).
	// Password > 128: argon2id na senha de 64 KB sem este guarda seria um vetor
	// de CPU-DoS (cada tentativa é ~300 ms + memória do argon2; handleRegister já
	// limita 8-128, logo nenhuma senha legítima passa disso). Identifier > 254:
	// comprimento máximo de um endereço de email (RFC 5321) e evita chaves Redis
	// de 64 KB no contador de lockout. Retornamos INVALID_CREDENTIALS (não
	// VALIDATION_ERROR) para não revelar o motivo da rejeição ao atacante.
	if ident == "" || len(ident) > 254 || len(body.Password) > 128 {
		writeErr(w, appErr(http.StatusUnauthorized, "INVALID_CREDENTIALS", "Email ou senha inválidos"))
		return
	}
	// Lockout chaveado por (IP + identifier), NÃO só pelo identifier: senão
	// qualquer um trancaria a vítima sabendo apenas o email (DoS de conta).
	// Com o IP na chave, o atacante só "tranca" a si mesmo contra aquela conta;
	// a proteção contra credential-stuffing distribuído continua vindo do
	// rate-limit por IP da rota (routes.go) + do limite global.
	lockKey := "api-go:login_fail:" + clientIP(r) + ":" + strings.ToLower(ident)
	lockN, lockErr := s.rdb.Get(r.Context(), lockKey).Int()
	if lockErr != nil && !errors.Is(lockErr, redis.Nil) {
		// Redis indisponível: fail-closed — não permitir tentativa sem confirmar o contador.
		slog.Error("login: erro Redis ao verificar lockout", "key", lockKey, "err", lockErr)
		writeErr(w, appErr(http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Serviço temporariamente indisponível"))
		return
	}
	if lockN >= maxLoginFails {
		writeErr(w, appErr(http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS", "Muitas tentativas. Tente novamente mais tarde."))
		return
	}
	acctLockKey := "api-go:login_fail_acct:" + strings.ToLower(ident)
	acctLockN, acctLockErr := s.rdb.Get(r.Context(), acctLockKey).Int()
	if acctLockErr != nil && !errors.Is(acctLockErr, redis.Nil) {
		slog.Error("login: erro Redis ao verificar lockout por conta", "key", acctLockKey, "err", acctLockErr)
		writeErr(w, appErr(http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Serviço temporariamente indisponível"))
		return
	}
	if acctLockN >= maxLoginFailsAccount {
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
	// de "senha incorreta" para quem mede latências. passOK é computado
	// ANTES do if para evitar que o short-circuit de `u == nil ||` impeça
	// a chamada ao argon2id quando o usuário não existe.
	hash := dummyPasswordHash
	if u != nil && u.PasswordHash != nil {
		hash = *u.PasswordHash
	}
	passOK := verifyPassword(body.Password, hash)
	if u == nil || u.LoginDisabled || u.PasswordHash == nil || !passOK {
		incrCmd := s.rdb.Incr(r.Context(), lockKey)
		if incrCmd.Err() != nil {
			slog.Warn("login_fail: Incr falhou; rejeitando (fail-closed)", "key", lockKey, "err", incrCmd.Err())
			writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro interno. Tente novamente."))
			return
		}
		if err := s.rdb.ExpireNX(r.Context(), lockKey, loginFailWindow).Err(); err != nil {
			slog.Warn("login_fail: ExpireNX falhou; contador de lockout pode não expirar", "key", lockKey, "err", err)
		}
		acctIncrCmd := s.rdb.Incr(r.Context(), acctLockKey)
		if acctIncrCmd.Err() != nil {
			slog.Warn("login_fail: Incr (conta) falhou; rejeitando (fail-closed)", "key", acctLockKey, "err", acctIncrCmd.Err())
			writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro interno. Tente novamente."))
			return
		}
		if err := s.rdb.ExpireNX(r.Context(), acctLockKey, loginFailAccountWindow).Err(); err != nil {
			slog.Warn("login_fail: ExpireNX (conta) falhou; contador de lockout pode não expirar", "key", acctLockKey, "err", err)
		}
		// Verifica o valor APÓS o Incr atômico para fechar a janela TOCTOU: dois
		// requests simultâneos podem ambos passar pelo Get pré-check (ambos veem
		// n=9), mas o Incr é atômico — o primeiro chega a 10, o segundo a 11.
		// Sem esta verificação, ambos retornariam INVALID_CREDENTIALS em vez de
		// TOO_MANY_ATTEMPTS, permitindo N extra tentativas iguais ao número de
		// goroutines concorrentes na janela argon2id (~300 ms).
		if incrCmd.Val() > maxLoginFails || acctIncrCmd.Val() > maxLoginFailsAccount {
			writeErr(w, appErr(http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS", "Muitas tentativas. Tente novamente mais tarde."))
			return
		}
		writeErr(w, appErr(http.StatusUnauthorized, "INVALID_CREDENTIALS", "Email ou senha inválidos"))
		return
	}
	if err := s.rdb.Del(r.Context(), lockKey).Err(); err != nil {
		slog.Warn("login: falha ao limpar contador de lockout no Redis", "key", lockKey, "err", err)
	}
	if err := s.rdb.Del(r.Context(), acctLockKey).Err(); err != nil {
		slog.Warn("login: falha ao limpar contador de lockout por conta no Redis", "key", acctLockKey, "err", err)
	}
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

	access, refresh, err := s.issueSession(r.Context(), w, r, u)
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := map[string]any{"user": s.buildProfile(r.Context(), u)}
	if isNativeClient(r) {
		resp["accessToken"] = access
		resp["refreshToken"] = refresh
	}
	writeJSON(w, http.StatusOK, resp)
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
	// resolveToken aceita JWT de sessão E PAT (st_...) — /auth/me precisa dos dois.
	// Para JWT já devolve o *User carregado do banco (suspensão já verificada);
	// para PAT devolve nil e buscamos abaixo.
	uid, u, err := s.resolveToken(r.Context(), token)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		// Caminho PAT: resolveToken não carrega o usuário completo.
		u, err = s.userByID(r.Context(), uid)
		if err != nil {
			writeErr(w, err)
			return
		}
		if u == nil {
			writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Usuário não encontrado"))
			return
		}
	}
	if u.SuspendedAt != nil {
		writeErr(w, appErr(http.StatusForbidden, "ACCOUNT_SUSPENDED", "Conta suspensa"))
		return
	}
	if err := s.rdb.Set(r.Context(), "user:last_seen:"+strconv.FormatInt(u.ID, 10), "1", 5*time.Minute).Err(); err != nil {
		slog.Warn("falha ao registrar último acesso", "uid", u.ID, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": s.buildProfile(r.Context(), u)})
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// viaCookie distingue o fluxo web (cookie httpOnly) do cliente nativo (sem
	// cookie jar, manda o refresh token via Authorization: Bearer). Não há risco de
	// um XSS forçar o caminho Bearer: a página não tem como LER o valor do cookie
	// httpOnly pra colocá-lo no header — só quem já guarda o token fora de cookie
	// (o app) consegue usar esse caminho.
	raw := ""
	viaCookie := false
	if c, err := r.Cookie("refresh_token"); err == nil && c.Value != "" {
		raw, viaCookie = c.Value, true
	}
	if raw == "" {
		if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && after != "" {
			raw = after
		}
	}
	if raw == "" {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Refresh token ausente"))
		return
	}
	uid, _, err := verifyToken(raw, s.cfg.JWTRefreshSecret, tokenTypeRefresh)
	if err != nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Refresh token inválido"))
		return
	}
	sid, _, expires, err := s.sessionByHash(r.Context(), hashRefreshToken(raw))
	if errors.Is(err, pgx.ErrNoRows) {
		// JWT de refresh criptograficamente válido (verifyToken já passou) mas sem
		// sessão correspondente no banco: a sessão já foi encerrada normalmente
		// (logout/reset de senha/suspensão) OU este é um refresh token JÁ
		// ROTACIONADO sendo reusado — indício de roubo (alguém copiou o token antes
		// da rotação e está tentando usá-lo depois que o dono renovou). Não dá pra
		// distinguir os dois casos aqui com certeza, então tratamos como suspeito
		// por precaução: revoga TODAS as sessões do usuário, não só esta. Um
		// logout/reset legítimo já não tem sessões pra revogar (no-op na prática);
		// só o caso de roubo real paga o preço de perder as outras sessões —
		// aceitável frente ao risco de acesso persistente indefinido. IMPORTANTE:
		// só entra aqui em ErrNoRows (linha realmente ausente) — um erro de banco
		// genérico (conexão instável, timeout) cai no ramo abaixo e NÃO revoga
		// nada, pra não derrubar sessões legítimas por uma falha transitória.
		if delErr := s.deleteUserSessions(r.Context(), uid); delErr != nil {
			slog.Error("refresh: falha ao revogar sessões após possível reuso de refresh token", "uid", uid, "err", delErr)
		} else {
			slog.Warn("refresh: refresh token sem sessão correspondente — sessões revogadas por precaução", "uid", uid)
		}
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Sessão expirada"))
		return
	}
	if err != nil {
		slog.Error("refresh: erro ao consultar sessão", "err", err)
		writeErr(w, appErr(http.StatusInternalServerError, "INTERNAL_ERROR", "Erro ao renovar sessão"))
		return
	}
	if expires.Before(time.Now()) {
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
	access, refresh, err := s.issueSession(r.Context(), w, r, u, sid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if viaCookie {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accessToken": access, "refreshToken": refresh})
}
