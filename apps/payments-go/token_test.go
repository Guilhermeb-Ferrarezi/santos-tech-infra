package main

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestVerifyToken(t *testing.T) {
	secret := "s3cr3t"
	claims := jwt.MapClaims{"sub": "42", "exp": time.Now().Add(time.Hour).Unix()}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))

	id, err := verifyToken(tok, secret)
	if err != nil || id != 42 {
		t.Fatalf("esperava id=42 err=nil, veio id=%d err=%v", id, err)
	}
	if _, err := verifyToken(tok, "outro"); err == nil {
		t.Fatal("esperava erro com secret errado")
	}
}
