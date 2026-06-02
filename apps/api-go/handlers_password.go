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
	if u != nil {
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
	html := fmt.Sprintf(`<p>Olá!</p>
<p>Recebemos uma solicitação para redefinir a senha da sua conta Santos Tech.</p>
<p><a href="%s" style="color:#187ABF">Clique aqui para criar uma nova senha</a></p>
<p>Este link expira em <strong>1 hora</strong>.</p>
<p>Se você não solicitou, ignore este email.</p>`, url)
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
	writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}
