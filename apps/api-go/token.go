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

// Valores do claim "typ", que separa access de refresh DENTRO do próprio token.
// Antes os dois JWTs carregavam exatamente as mesmas claims (sub/iat/exp) e só o
// segredo os distinguia: nada no código impede que JWT_SECRET e
// JWT_REFRESH_SECRET recebam o mesmo valor, e nesse caso o refresh de 7 dias
// passaria a valer como access token.
const (
	tokenTypeAccess  = "access"
	tokenTypeRefresh = "refresh"
)

// generateTokens cria access + refresh JWT (HS256), compatível com a versão jose/TS.
func generateTokens(accessSecret, refreshSecret string, userID int64, email, name string) (access, refresh string, err error) {
	now := time.Now()
	sub := strconv.FormatInt(userID, 10)

	accessClaims := jwt.MapClaims{"sub": sub, "iat": now.Unix(), "exp": now.Add(accessTTL).Unix(), "typ": tokenTypeAccess}
	if email != "" {
		accessClaims["email"] = email
	}
	if name != "" {
		accessClaims["name"] = name
	}
	access, err = jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString([]byte(accessSecret))
	if err != nil {
		return
	}

	refreshClaims := jwt.MapClaims{"sub": sub, "iat": now.Unix(), "exp": now.Add(refreshTTL).Unix(), "typ": tokenTypeRefresh}
	refresh, err = jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString([]byte(refreshSecret))
	return
}

// verifyToken valida um JWT HS256 e retorna o userID (claim sub) e o nome
// (claim name, vazio se ausente — ex: refresh tokens não carregam nome).
//
// want é o tipo de token exigido (tokenTypeAccess/tokenTypeRefresh): um token
// com "typ" diferente do esperado é recusado, mesmo que a assinatura confira.
// Tokens SEM o claim "typ" são aceitos — são os emitidos antes desta mudança, e
// recusá-los derrubaria todas as sessões vivas (access de 2h, refresh de 7 dias)
// e os tokens do fluxo OAuth. A checagem passa a valer de fato conforme os
// tokens antigos vencem.
func verifyToken(token, secret, want string) (id int64, name string, err error) {
	t, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok || t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("método de assinatura inesperado")
		}
		return []byte(secret), nil
	})
	if err != nil || !t.Valid {
		return 0, "", errors.New("token inválido")
	}
	claims, ok := t.Claims.(jwt.MapClaims)
	if !ok {
		return 0, "", errors.New("claims inválidas")
	}
	if typ, _ := claims["typ"].(string); typ != "" && typ != want {
		return 0, "", errors.New("tipo de token inesperado")
	}
	sub, _ := claims["sub"].(string)
	id, err = strconv.ParseInt(sub, 10, 64)
	if err != nil {
		return 0, "", errors.New("sub inválido")
	}
	name, _ = claims["name"].(string)
	return id, name, nil
}
