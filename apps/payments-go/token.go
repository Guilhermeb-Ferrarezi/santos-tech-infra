package main

import (
	"errors"
	"strconv"

	"github.com/golang-jwt/jwt/v5"
)

// verifyToken valida um JWT HS256 e retorna o userID (claim sub). Espelha o token.go do api-go.
func verifyToken(token, secret string) (int64, error) {
	t, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok || t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("alg inesperado")
		}
		return []byte(secret), nil
	})
	if err != nil || !t.Valid {
		return 0, errors.New("token inválido")
	}
	claims, ok := t.Claims.(jwt.MapClaims)
	if !ok {
		return 0, errors.New("claims inválidas")
	}
	sub, _ := claims["sub"].(string)
	id, err := strconv.ParseInt(sub, 10, 64)
	if err != nil {
		return 0, errors.New("sub inválido")
	}
	return id, nil
}
