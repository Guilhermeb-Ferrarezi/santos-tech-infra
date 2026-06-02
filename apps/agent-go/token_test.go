package main

import (
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func signHS256(t *testing.T, secret string, uid int64) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub": strconv.FormatInt(uid, 10),
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tok
}

func TestVerifyTokenValid(t *testing.T) {
	secret := "segredo-compartilhado-do-auth"
	tok := signHS256(t, secret, 42)
	uid, err := verifyToken(tok, secret)
	if err != nil {
		t.Fatalf("verifyToken: %v", err)
	}
	if uid != 42 {
		t.Fatalf("esperava 42, veio %d", uid)
	}
}

func TestVerifyTokenWrongSecret(t *testing.T) {
	tok := signHS256(t, "segredo-certo", 1)
	if _, err := verifyToken(tok, "segredo-errado"); err == nil {
		t.Fatal("verifyToken deveria falhar com secret errado")
	}
}

func TestVerifyTokenRejectsNone(t *testing.T) {
	// Token "alg: none" não deve ser aceito.
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"sub": "1"}).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := verifyToken(tok, "qualquer"); err == nil {
		t.Fatal("verifyToken deveria rejeitar alg=none")
	}
}
