package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestVerifyPKCE(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])

	if !verifyPKCE(verifier, challenge) {
		t.Fatal("verifier correto deveria validar")
	}
	if verifyPKCE("outro-verifier", challenge) {
		t.Fatal("verifier errado não deveria validar")
	}
	if verifyPKCE("", challenge) || verifyPKCE(verifier, "") {
		t.Fatal("vazio não deveria validar")
	}
}

func TestAuthCodeKeyHashesCode(t *testing.T) {
	if authCodeKey("abc") == "oauth:code:abc" {
		t.Fatal("a chave deve usar o hash do code, nunca o code em claro")
	}
	if authCodeKey("abc") != "oauth:code:"+sha256Hex("abc") {
		t.Fatal("chave inesperada")
	}
}

// O token do /oauth/token não pode ser indistinguível da sessão do painel:
// ele sai marcado com aud=<client_id> e scope.
func TestGenerateOAuthTokensMarcaAudEScope(t *testing.T) {
	access, refresh, err := generateOAuthTokens("s-access", "s-refresh", 42, "u@x.com", "Alice", "dcr_abc123")
	if err != nil {
		t.Fatalf("generateOAuthTokens: %v", err)
	}
	if got := tokenAudience(access, "s-access"); got != "dcr_abc123" {
		t.Errorf("aud do access = %q", got)
	}
	// O aud precisa sobreviver à rotação: o refresh também carrega o client.
	if got := tokenAudience(refresh, "s-refresh"); got != "dcr_abc123" {
		t.Errorf("aud do refresh = %q", got)
	}
	// Retrocompatível: continua sendo um JWT HS256 válido com o mesmo sub.
	uid, name, err := verifyToken(access, "s-access", tokenTypeAccess)
	if err != nil || uid != 42 || name != "Alice" {
		t.Errorf("verifyToken(access) = %d, %q, %v", uid, name, err)
	}
	claims := decodeJWTClaims(t, access)
	if claims["scope"] != oauthTokenScope {
		t.Errorf("scope = %v", claims["scope"])
	}
}

// Sessão normal do painel nunca tem aud — é assim que o authGuard distingue
// um token OAuth de uma sessão quando OAUTH_AUD_ENFORCE=1.
func TestTokenAudienceVazioParaSessaoDoPainel(t *testing.T) {
	access, _, err := generateTokens("s-access", "s-refresh", 7, "u@x.com", "Bob")
	if err != nil {
		t.Fatalf("generateTokens: %v", err)
	}
	if got := tokenAudience(access, "s-access"); got != "" {
		t.Errorf("sessão do painel não deveria ter aud, veio %q", got)
	}
	// Segredo errado / token lixo → vazio (fail-open aqui é seguro: quem
	// decide validade é o verifyToken do caller).
	if got := tokenAudience(access, "outro-segredo"); got != "" {
		t.Errorf("segredo errado deveria dar aud vazio, veio %q", got)
	}
	if got := tokenAudience("nao-e-jwt", "s-access"); got != "" {
		t.Errorf("token lixo deveria dar aud vazio, veio %q", got)
	}
}

// decodeJWTClaims lê o payload de um JWT sem validar assinatura — só pros
// testes conferirem claims que não têm getter dedicado.
func decodeJWTClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token não tem 3 partes: %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("payload base64: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("payload json: %v", err)
	}
	return m
}
