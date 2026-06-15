package main

import (
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	accessTTL  = 2 * time.Hour
	refreshTTL = 7 * 24 * time.Hour
)

// generateTokens cria access + refresh JWT (HS256), compatível com a versão jose/TS.
func generateTokens(accessSecret, refreshSecret string, userID int64, email string) (access, refresh string, err error) {
	now := time.Now()
	sub := strconv.FormatInt(userID, 10)

	accessClaims := jwt.MapClaims{"sub": sub, "iat": now.Unix(), "exp": now.Add(accessTTL).Unix()}
	if email != "" {
		accessClaims["email"] = email
	}
	access, err = jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString([]byte(accessSecret))
	if err != nil {
		return
	}

	refreshClaims := jwt.MapClaims{"sub": sub, "iat": now.Unix(), "exp": now.Add(refreshTTL).Unix()}
	refresh, err = jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString([]byte(refreshSecret))
	return
}

// verifyToken valida um JWT HS256 e retorna o userID (claim sub).
func verifyToken(token, secret string) (int64, error) {
	t, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok || t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("método de assinatura inesperado")
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
