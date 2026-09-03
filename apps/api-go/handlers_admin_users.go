package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var localPartRe = regexp.MustCompile(`^[a-z0-9._%+-]+$`)

var emailRe = regexp.MustCompile(`^[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}$`)

// handleCreateAdminUser cria um usuário sem senha e dispara o convite. Aceita
// `email` completo (qualquer domínio) ou `localPart` (vira @santos-tech.com);
// `email` tem precedência se os dois vierem. Com `shared=true`, cria uma caixa
// institucional @santos-tech.com sem login e sem convite. Com `password` (não-
// institucional), cria a conta já ATIVA com essa senha sem enviar convite.
func (s *Server) handleCreateAdminUser(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		Email     string `json:"email"`
		LocalPart string `json:"localPart"`
		Name      string `json:"name"`
		Role      int16  `json:"role"`
		Shared    bool   `json:"shared"`
		Password  string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	body.Email = strings.TrimSpace(strings.ToLower(body.Email))
	body.LocalPart = strings.TrimSpace(strings.ToLower(body.LocalPart))
	body.Name = strings.TrimSpace(body.Name)

	// Conta institucional (caixa compartilhada, sem login): exige localPart
	// (@santos-tech.com), nome opcional (default = localPart) e NÃO recebe convite.
	if body.Shared {
		if body.LocalPart == "" {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "conta institucional exige usuário (localPart)"))
			return
		}
		if !localPartRe.MatchString(body.LocalPart) {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "localPart inválido (sem @ ou espaços)"))
			return
		}
		email := body.LocalPart + "@" + staffDomain
		name := body.Name
		if name == "" {
			name = body.LocalPart
		}
		existing, err := s.userByEmail(r.Context(), email)
		if err != nil {
			writeErr(w, err)
			return
		}
		if existing != nil {
			writeErr(w, appErr(http.StatusConflict, "EMAIL_ALREADY_EXISTS", "Este email já está cadastrado"))
			return
		}
		u, err := s.insertSharedMailbox(r.Context(), email, name)
		if err != nil {
			writeErr(w, err)
			return
		}
		s.portalSyncUserBestEffort(r.Context(), u)
		s.provisionMailboxBestEffort(r.Context(), u)
		writeJSON(w, http.StatusCreated, map[string]any{"user": adminUserJSON(u)})
		return
	}

	if body.Name == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "nome é obrigatório"))
		return
	}
	if len(body.Name) > 128 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "nome deve ter no máximo 128 caracteres"))
		return
	}
	var email string
	switch {
	case body.Email != "":
		if len(body.Email) > 254 || !emailRe.MatchString(body.Email) {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "email inválido"))
			return
		}
		email = body.Email
	case body.LocalPart != "":
		if !localPartRe.MatchString(body.LocalPart) {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "localPart inválido (sem @ ou espaços)"))
			return
		}
		email = body.LocalPart + "@" + staffDomain
	default:
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "informe email ou localPart"))
		return
	}
	if body.Role == 0 {
		body.Role = RoleStudent
	}
	if body.Role < RoleStudent || body.Role > RoleAdmin {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "role inválido"))
		return
	}
	// Valida tamanho da senha antes de tocar no banco.
	if body.Password != "" && (len(body.Password) < 8 || len(body.Password) > 128) {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "senha deve ter entre 8 e 128 caracteres"))
		return
	}

	existing, err := s.userByEmail(r.Context(), email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if existing != nil {
		writeErr(w, appErr(http.StatusConflict, "EMAIL_ALREADY_EXISTS", "Este email já está cadastrado"))
		return
	}

	// Se uma senha foi fornecida, cria a conta já ativa (sem convite).
	if body.Password != "" {
		pwdHash, err := hashPassword(body.Password)
		if err != nil {
			writeErr(w, err)
			return
		}
		u, err := s.insertUserWithRoleAndPassword(r.Context(), email, body.Name, pwdHash, body.Role)
		if err != nil {
			writeErr(w, err)
			return
		}
		s.portalSyncUserBestEffort(r.Context(), u)
		s.provisionMailboxBestEffort(r.Context(), u)
		writeJSON(w, http.StatusCreated, map[string]any{"user": adminUserJSON(u)})
		return
	}

	u, err := s.insertUserWithRole(r.Context(), email, body.Name, body.Role)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.sendInvite(r.Context(), u) // best-effort: loga erro, não falha a criação
	s.portalSyncUserBestEffort(r.Context(), u)
	s.provisionMailboxBestEffort(r.Context(), u)
	writeJSON(w, http.StatusCreated, map[string]any{"user": adminUserJSON(u)})
}

// handleListAdminUsers lista usuários: por padrão só @santos-tech.com (compat
// com o painel do auth); com ?scope=all, todos os cadastrados.
func (s *Server) handleListAdminUsers(w http.ResponseWriter, r *http.Request) {
	var (
		users []User
		err   error
	)
	if r.URL.Query().Get("scope") == "all" {
		users, err = s.listAllUsers(r.Context())
	} else {
		users, err = s.listUsersByDomain(r.Context(), staffDomain)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(users))
	for i := range users {
		out = append(out, adminUserJSON(&users[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

// handleUpdateAdminUser atualiza nome/role e/ou suspende/reativa. Aceita também
// `password` (opcional) para redefinir a senha diretamente sem link de reset.
func (s *Server) handleUpdateAdminUser(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "id inválido"))
		return
	}
	var body struct {
		Name         *string `json:"name"`
		Role         *int16  `json:"role"`
		Suspended    *bool   `json:"suspended"`
		QuotaBytes   *int64  `json:"quotaBytes"`
		CustomRoleID *string `json:"customRoleId"`
		Password     string  `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if body.Name != nil && len(*body.Name) > 128 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "nome deve ter no máximo 128 caracteres"))
		return
	}
	if body.Role != nil && (*body.Role < RoleStudent || *body.Role > RoleCustom) {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "role inválido"))
		return
	}
	if body.Role != nil && *body.Role == RoleCustom && (body.CustomRoleID == nil || !isValidUUID(*body.CustomRoleID)) {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "customRoleId obrigatório e deve ser UUID válido para role=4"))
		return
	}
	if body.QuotaBytes != nil && *body.QuotaBytes < 0 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "quotaBytes inválido"))
		return
	}
	if body.Password != "" && (len(body.Password) < 8 || len(body.Password) > 128) {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "senha deve ter entre 8 e 128 caracteres"))
		return
	}
	// Self-proteção: admin não pode se auto-suspender (evita lockout).
	if body.Suspended != nil && *body.Suspended && id == userIDFrom(r) {
		writeErr(w, appErr(http.StatusBadRequest, "SELF_ACTION", "você não pode suspender a própria conta"))
		return
	}
	// Self-proteção: admin não pode alterar o próprio role (evita se rebaixar
	// e, se for o único admin, travar o sistema sem ninguém com adminGuard).
	if body.Role != nil && *body.Role != RoleAdmin && id == userIDFrom(r) {
		writeErr(w, appErr(http.StatusBadRequest, "SELF_ACTION", "você não pode alterar o próprio papel"))
		return
	}
	if body.Suspended != nil {
		if err := s.setUserSuspended(r.Context(), id, *body.Suspended); err != nil {
			writeErr(w, err)
			return
		}
	}
	// Hash de senha antecipado (CPU-bound; fora da transação).
	var pwdHash string
	if body.Password != "" {
		h, err := hashPassword(body.Password)
		if err != nil {
			writeErr(w, err)
			return
		}
		pwdHash = h
	}
	// Atualiza senha + revoga sessões + campos de admin numa única transação,
	// garantindo que nenhuma etapa fique parcialmente aplicada em caso de erro.
	u, err := s.updateAdminUserFull(r.Context(), id, pwdHash, body.Name, body.Role, body.QuotaBytes, body.CustomRoleID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "usuário não encontrado"))
		return
	}
	s.portalSyncUserBestEffort(r.Context(), u)
	writeJSON(w, http.StatusOK, map[string]any{"user": adminUserJSON(u)})
}

// handleInviteAdminUser reenvia o convite para um usuário existente.
func (s *Server) handleInviteAdminUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "id inválido"))
		return
	}
	u, err := s.userByID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "usuário não encontrado"))
		return
	}
	if u.LoginDisabled {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "conta institucional não tem login — não há convite"))
		return
	}
	s.sendInvite(r.Context(), u)
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteAdminUser exclui um usuário.
func (s *Server) handleDeleteAdminUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "id inválido"))
		return
	}
	if id == userIDFrom(r) {
		writeErr(w, appErr(http.StatusBadRequest, "SELF_ACTION", "você não pode excluir a própria conta"))
		return
	}
	if err := s.deleteUser(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSendResetAdminUser gera e envia um link de redefinição de senha para um
// usuário existente. Reutiliza exatamente o mesmo mecanismo do forgot-password
// (TTL 1h, mesmo template). Body opcional: {"email"?: string} — se ausente, usa
// o email cadastrado do usuário.
func (s *Server) handleSendResetAdminUser(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "id inválido"))
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	// body é opcional; ignora erro de EOF (body vazio)
	_ = decodeJSON(r, &body)
	body.Email = strings.TrimSpace(strings.ToLower(body.Email))

	u, err := s.userByID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "usuário não encontrado"))
		return
	}
	if u.LoginDisabled {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "conta institucional não tem login — sem link de reset"))
		return
	}

	// Destino do email: usa o fornecido no body ou o do próprio usuário.
	to := body.Email
	if to == "" {
		to = u.Email
	} else if !emailRe.MatchString(to) {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "email inválido"))
		return
	}

	// Mesmo mecanismo do forgot-password: token efêmero no Redis (TTL 1h).
	token := randomToken(32)
	hash := sha256Hex(token)
	if err := s.rdb.Set(r.Context(), "pwd_reset:"+hash, u.ID, time.Hour).Err(); err != nil {
		writeErr(w, err)
		return
	}
	resetURL := fmt.Sprintf("%s/reset-password?token=%s", s.cfg.AuthWebOrigin, token)
	s.sendResetEmail(to, resetURL)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sendInvite gera um token de convite (mesmo mecanismo do reset de senha, TTL 72h)
// e envia o email com o link para a pessoa definir a própria senha.
func (s *Server) sendInvite(ctx context.Context, u *User) {
	token := randomToken(32)
	hash := sha256Hex(token)
	if err := s.rdb.Set(ctx, "pwd_reset:"+hash, u.ID, 72*time.Hour).Err(); err != nil {
		slog.Error("falha ao gravar token de convite", "err", err, "user", u.ID)
		return
	}
	url := fmt.Sprintf("%s/reset-password?token=%s", s.cfg.AuthWebOrigin, token)
	s.sendInviteEmail(u.Email, u.Name, url)
}

// sendInviteEmail enfileira (fila durável) o email de convite (definição da
// primeira senha). Antes era `go s.sendInviteEmail(...)`, que se perdia num
// restart — agora vira task asynq com retry.
func (s *Server) sendInviteEmail(to, name, url string) {
	html := emailLayout(emailGreeting(name) + fmt.Sprintf(`<p style="margin:0">Você foi convidado para acessar a <strong>plataforma Santos Tech</strong>. Defina sua senha para ativar a conta:</p>
%s
<p style="margin:0;color:#496B84;font-size:13px">Este link expira em <strong>72 horas</strong>. Se você não esperava este convite, ignore este email.</p>
%s`, emailButton(url, "Definir minha senha"), emailLinkFallback(url)))
	s.enqueueEmail("sendInviteEmail", to, "Seu acesso à Santos Tech", html)
}

// portalSyncUserBestEffort espelha o usuário no Portal (ver portalSyncUserFromAuth)
// sem propagar erro — o Portal é um consumidor externo do auth central, uma falha
// dele não pode quebrar criar/atualizar um usuário no auth.
func (s *Server) portalSyncUserBestEffort(ctx context.Context, u *User) {
	if err := s.portalSyncUserFromAuth(ctx, u.Name, u.Email, u.Role); err != nil {
		slog.Error("falha ao sincronizar usuário com o portal", "err", err, "user", u.ID)
	}
}

// provisionMailboxBestEffort cria a caixa de email do usuário só quando o
// endereço é @santos-tech.com (o mailserver não hospeda domínio externo tipo
// gmail.com) — best-effort, mesmo padrão do sync com o Portal.
//
// Roda em background (safeGo + contexto descolado da requisição): o
// provisionamento chama e.client, que tem 20s de timeout — chamado direto no
// caminho da requisição, isso prendia handleCreateAdminUser até 20s se o
// email-sender estivesse lento/fora, mesmo essa função dizendo (e o caller
// esperando) que "nunca bloqueia a operação de auth".
func (s *Server) provisionMailboxBestEffort(ctx context.Context, u *User) {
	if !strings.HasSuffix(strings.ToLower(u.Email), "@"+staffDomain) {
		return
	}
	bg := context.WithoutCancel(ctx)
	safeGo("provisionMailbox", func() {
		if err := s.email.provisionMailbox(bg, u.Email); err != nil {
			slog.Error("falha ao provisionar caixa de email", "err", err, "user", u.ID)
		}
	})
}

// adminUserJSON é a forma pública de um usuário nas rotas admin.
func adminUserJSON(u *User) map[string]any {
	return map[string]any{
		"id":            u.ID,
		"email":         u.Email,
		"name":          u.Name,
		"avatarUrl":     u.AvatarURL,
		"role":          u.Role,
		"customRoleId":  u.CustomRoleID,
		"suspendedAt":   u.SuspendedAt,
		"createdAt":     u.CreatedAt.UTC().Format(time.RFC3339),
		"mfaEnabled":    u.MFAEnabled,
		"quotaBytes":    u.QuotaBytes,
		"pending":       u.PasswordHash == nil, // convite não aceito (sem senha definida)
		"loginDisabled": u.LoginDisabled,
	}
}
