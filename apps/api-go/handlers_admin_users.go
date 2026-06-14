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
// institucional @santos-tech.com sem login e sem convite.
func (s *Server) handleCreateAdminUser(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		Email     string `json:"email"`
		LocalPart string `json:"localPart"`
		Name      string `json:"name"`
		Role      int16  `json:"role"`
		Shared    bool   `json:"shared"`
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
		writeJSON(w, http.StatusCreated, map[string]any{"user": adminUserJSON(u)})
		return
	}

	if body.Name == "" {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "nome é obrigatório"))
		return
	}
	var email string
	switch {
	case body.Email != "":
		if !emailRe.MatchString(body.Email) {
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

	existing, err := s.userByEmail(r.Context(), email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if existing != nil {
		writeErr(w, appErr(http.StatusConflict, "EMAIL_ALREADY_EXISTS", "Este email já está cadastrado"))
		return
	}
	u, err := s.insertUserWithRole(r.Context(), email, body.Name, body.Role)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.sendInvite(r.Context(), u) // best-effort: loga erro, não falha a criação
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

// handleUpdateAdminUser atualiza nome/role e/ou suspende/reativa.
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
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
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
	// Self-proteção: admin não pode se auto-suspender (evita lockout).
	if body.Suspended != nil && *body.Suspended && id == userIDFrom(r) {
		writeErr(w, appErr(http.StatusBadRequest, "SELF_ACTION", "você não pode suspender a própria conta"))
		return
	}
	if body.Suspended != nil {
		if err := s.setUserSuspended(r.Context(), id, *body.Suspended); err != nil {
			writeErr(w, err)
			return
		}
	}
	u, err := s.updateUserAdmin(r.Context(), id, body.Name, body.Role, body.QuotaBytes, body.CustomRoleID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "usuário não encontrado"))
		return
	}
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
	go s.sendInviteEmail(u.Email, u.Name, url)
}

func (s *Server) sendInviteEmail(to, name, url string) {
	html := emailLayout(emailGreeting(name) + fmt.Sprintf(`<p style="margin:0">Você foi convidado para acessar a <strong>plataforma Santos Tech</strong>. Defina sua senha para ativar a conta:</p>
%s
<p style="margin:0;color:#496B84;font-size:13px">Este link expira em <strong>72 horas</strong>. Se você não esperava este convite, ignore este email.</p>
%s`, emailButton(url, "Definir minha senha"), emailLinkFallback(url)))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := s.email.send(ctx, to, "Seu acesso à Santos Tech", html); err != nil {
		slog.Error("falha ao enviar email de convite", "err", err)
	}
}

// adminUserJSON é a forma pública de um usuário nas rotas admin.
func adminUserJSON(u *User) map[string]any {
	return map[string]any{
		"id":            u.ID,
		"email":         u.Email,
		"name":          u.Name,
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
