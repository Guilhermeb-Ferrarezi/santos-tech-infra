package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	email := strings.TrimSpace(strings.ToLower(body.Email))
	u, err := s.userByEmail(r.Context(), email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u != nil && !u.LoginDisabled {
		token := randomToken(32)
		hash := sha256Hex(token)
		if err := s.rdb.Set(r.Context(), "pwd_reset:"+hash, u.ID, time.Hour).Err(); err != nil {
			writeErr(w, err)
			return
		}
		resetURL := fmt.Sprintf("%s/reset-password?token=%s", s.cfg.AuthWebOrigin, token)
		go s.sendResetEmail(email, resetURL)
	}
	// resposta sempre igual (não vaza se o email existe)
	writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}

func (s *Server) sendResetEmail(to, url string) {
	html := emailLayout(emailGreeting("") + fmt.Sprintf(`<p style="margin:0">Recebemos uma solicitação para redefinir a senha da sua conta <strong>Santos Tech</strong>:</p>
%s
<p style="margin:0;color:#496B84;font-size:13px">Este link expira em <strong>1 hora</strong>. Se você não solicitou, ignore este email — sua senha continua a mesma.</p>
%s`, emailButton(url, "Criar nova senha"), emailLinkFallback(url)))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := s.email.send(ctx, to, "Recuperação de senha — Santos Tech", html); err != nil {
		slog.Error("falha ao enviar email de recuperação", "err", err)
	}
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token       string `json:"token"`
		NewPassword string `json:"newPassword"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}
	if len(body.NewPassword) < 8 {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "senha mínima de 8 caracteres"))
		return
	}
	hash := sha256Hex(body.Token)
	idStr, err := s.rdb.Get(r.Context(), "pwd_reset:"+hash).Result()
	if err != nil || idStr == "" {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_TOKEN", "Link de recuperação inválido ou expirado"))
		return
	}
	uid, _ := strconv.ParseInt(idStr, 10, 64)
	// Antes de gravar: se o usuário ainda não tinha senha (convite pendente),
	// esta é a PRIMEIRA senha — manda o email de "conta ativada" depois.
	u, err := s.userByID(r.Context(), uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u != nil && u.LoginDisabled {
		writeErr(w, appErr(http.StatusBadRequest, "INVALID_TOKEN", "Link de recuperação inválido ou expirado"))
		return
	}
	firstPassword := u != nil && u.PasswordHash == nil
	newHash, err := hashPassword(body.NewPassword)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.updatePassword(r.Context(), uid, newHash); err != nil {
		writeErr(w, err)
		return
	}
	_ = s.deleteUserSessions(r.Context(), uid)
	s.rdb.Del(r.Context(), "pwd_reset:"+hash)
	if firstPassword {
		go s.sendWelcomeEmail(u.Email, u.Name)
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}

// sendWelcomeEmail confirma a ativação da conta quando a pessoa define a senha
// pela primeira vez (convite aceito).
func (s *Server) sendWelcomeEmail(to, name string) {
	html := emailLayout(emailGreeting(name) + fmt.Sprintf(`<p style="margin:0">Sua senha foi criada e a sua conta <strong>Santos Tech</strong> está ativa. 🎉</p>
%s
<p style="margin:0 0 8px">Entre com o seu email e a senha que você acabou de definir. Esse mesmo login dá acesso à sua caixa de email <strong>@santos-tech.com</strong>.</p>
<p style="margin:0;color:#496B84;font-size:13px">Se não foi você quem ativou a conta, avise um administrador imediatamente.</p>`,
		emailButton(s.cfg.AuthWebOrigin, "Entrar na plataforma")))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := s.email.send(ctx, to, "Conta ativada — Santos Tech", html); err != nil {
		slog.Error("falha ao enviar email de boas-vindas", "err", err)
	}
}
