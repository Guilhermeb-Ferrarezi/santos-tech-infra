package main

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	tAccess  = "access-secret-xyz"
	tRefresh = "refresh-secret-xyz"
)

func TestGenerateAndVerifyToken(t *testing.T) {
	access, refresh, err := generateTokens(tAccess, tRefresh, 42, "user@x.com", "Alice")
	if err != nil {
		t.Fatalf("generateTokens: %v", err)
	}
	if uid, _, err := verifyToken(access, tAccess); err != nil || uid != 42 {
		t.Fatalf("verify access: uid=%d err=%v", uid, err)
	}
	if uid, _, err := verifyToken(refresh, tRefresh); err != nil || uid != 42 {
		t.Fatalf("verify refresh: uid=%d err=%v", uid, err)
	}
}

func TestVerifyTokenWrongSecret(t *testing.T) {
	access, _, _ := generateTokens(tAccess, tRefresh, 1, "", "")
	if _, _, err := verifyToken(access, "outro-secret"); err == nil {
		t.Fatal("esperava erro com secret errado")
	}
}

// access e refresh usam secrets diferentes: um não pode validar o outro.
func TestVerifyTokenSecretSeparation(t *testing.T) {
	access, refresh, _ := generateTokens(tAccess, tRefresh, 1, "", "")
	if _, _, err := verifyToken(access, tRefresh); err == nil {
		t.Error("access aceito com refresh secret")
	}
	if _, _, err := verifyToken(refresh, tAccess); err == nil {
		t.Error("refresh aceito com access secret")
	}
}

func TestVerifyTokenMalformed(t *testing.T) {
	for _, tok := range []string{"", "abc", "a.b.c", "not-a-jwt"} {
		if _, _, err := verifyToken(tok, tAccess); err == nil {
			t.Errorf("esperava erro p/ token %q", tok)
		}
	}
}

func TestVerifyTokenExpired(t *testing.T) {
	claims := jwt.MapClaims{
		"sub": "5",
		"iat": time.Now().Add(-2 * time.Hour).Unix(),
		"exp": time.Now().Add(-time.Hour).Unix(),
	}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(tAccess))
	if _, _, err := verifyToken(tok, tAccess); err == nil {
		t.Fatal("token expirado foi aceito")
	}
}

// proteção contra alg-confusion: token "none" (sem assinatura) deve ser rejeitado.
func TestVerifyTokenAlgNone(t *testing.T) {
	claims := jwt.MapClaims{"sub": "5", "exp": time.Now().Add(time.Hour).Unix()}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Skipf("none signing indisponível: %v", err)
	}
	if _, _, err := verifyToken(tok, tAccess); err == nil {
		t.Fatal("token 'none' foi aceito")
	}
}

func TestVerifyTokenNonNumericSub(t *testing.T) {
	claims := jwt.MapClaims{"sub": "não-numérico", "exp": time.Now().Add(time.Hour).Unix()}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(tAccess))
	if _, _, err := verifyToken(tok, tAccess); err == nil {
		t.Fatal("sub não-numérico foi aceito")
	}
}

func TestVerifyTokenNameClaim(t *testing.T) {
	access, _, err := generateTokens(tAccess, tRefresh, 7, "u@x.com", "João Silva")
	if err != nil {
		t.Fatalf("generateTokens: %v", err)
	}
	uid, name, err := verifyToken(access, tAccess)
	if err != nil || uid != 7 || name != "João Silva" {
		t.Fatalf("verify name: uid=%d name=%q err=%v", uid, name, err)
	}
}
