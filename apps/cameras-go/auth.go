package main

import (
	"context"
	"net/http"
)

type ctxKey string

const userIDKey ctxKey = "userID"
const userNameKey ctxKey = "userName"

func (s *Server) authGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if tok == "" {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Não autenticado")
			return
		}
		uid, err := verifyToken(tok, s.cfg.JWTSecret)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Token inválido ou expirado")
			return
		}

		// Injeta no request
		ctx := context.WithValue(r.Context(), userIDKey, uid)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		uid := r.Context().Value(userIDKey).(int64)
		var role int
		var name string

		// Cameras-go precisa validar a permissão no banco compartilhado de users (santos-tech principal)
		// Como não temos esse banco diretamente aqui (ele fica no api-go), devemos usar um webhook ou
		// apenas validar o JWT claims caso ele tivesse `role`.
		// Mas o authGuard do api-go checa direto na tabela `users`.
		// Aqui, para ser rápido e seguro: ou acessamos a DB `DATABASE_URL` (se for o mesmo banco)
		// ou adicionamos `role` ao JWT. Supondo que `cfg.DatabaseURL` aponta para o mesmo banco postgres.
		err := s.db.QueryRow(r.Context(), `SELECT role, name FROM users WHERE id=$1`, uid).Scan(&role, &name)
		if err != nil || role != 3 {
			writeError(w, http.StatusForbidden, "forbidden", "Acesso restrito a administradores")
			return
		}

		ctx := context.WithValue(r.Context(), userNameKey, name)
		next(w, r.WithContext(ctx))
	})
}
