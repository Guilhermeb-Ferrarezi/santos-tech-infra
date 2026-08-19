package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Fluxo OAuth 2.0 authorization code + PKCE (S256) — só apps internos por ora.
// Estado efêmero no Redis; tokens finais são os mesmos JWTs HS256 da sessão.

// authRequest é uma autorização pendente (Redis oauth:authreq:<id>, TTL 10min).
type authRequest struct {
	ClientID      string `json:"clientId"`
	ClientName    string `json:"clientName"`
	RedirectURI   string `json:"redirectUri"`
	State         string `json:"state"`
	CodeChallenge string `json:"codeChallenge"`
}

// authCode é um code emitido (Redis oauth:code:<sha256>, TTL 60s, uso único).
type authCode struct {
	ClientID      string `json:"clientId"`
	RedirectURI   string `json:"redirectUri"`
	UserID        int64  `json:"userId"`
	CodeChallenge string `json:"codeChallenge"`
}

const (
	authReqTTL  = 10 * time.Minute
	authCodeTTL = time.Minute
)

func authReqKey(id string) string { return "oauth:authreq:" + id }

// authCodeKey guarda pelo HASH do code — vazamento do Redis não vaza codes.
func authCodeKey(code string) string { return "oauth:code:" + sha256Hex(code) }

// verifyPKCE confere challenge == base64url(sha256(verifier)), sem padding (S256).
func verifyPKCE(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}
	h := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(h[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

// ── Tokens emitidos pelo /oauth/token ────────────────────────────────────────
//
// Historicamente o /oauth/token devolvia EXATAMENTE o mesmo JWT da sessão do
// painel: sem aud, sem scope. Qualquer app OAuth (inclusive um registrado por
// DCR anônimo) recebia, na prática, uma sessão completa aceita pelo authGuard
// em todas as rotas — inclusive as de admin. Agora o token sai marcado:
//
//   aud   = client_id do app OAuth
//   scope = o que o token realmente concede hoje
//
// A RECUSA desses tokens nas rotas de sessão do painel fica atrás da flag
// OAUTH_AUD_ENFORCE=1 (default DESLIGADO) — ligar de imediato quebraria
// clients e o app mobile que hoje usam o token OAuth como sessão. A marcação
// é retrocompatível: um claim a mais que ninguém valida ainda.

// oauthTokenScope descreve o que o token OAuth concede de fato: os claims de
// perfil devolvidos pelo /oauth/userinfo. Não há negociação de escopo no
// /oauth/authorize — anunciar mais que isso seria mentira.
const oauthTokenScope = "profile email"

// generateOAuthTokens emite o par access/refresh do fluxo OAuth com aud e
// scope. Fora isso é idêntico ao generateTokens da sessão (HS256, mesmos
// segredos e TTLs) — o refresh também leva aud pra que a rotação preserve a
// identidade do client.
func generateOAuthTokens(accessSecret, refreshSecret string, userID int64, email, name, clientID string) (access, refresh string, err error) {
	now := time.Now()
	sub := strconv.FormatInt(userID, 10)

	accessClaims := jwt.MapClaims{"sub": sub, "iat": now.Unix(), "exp": now.Add(accessTTL).Unix()}
	if email != "" {
		accessClaims["email"] = email
	}
	if name != "" {
		accessClaims["name"] = name
	}
	if clientID != "" {
		accessClaims["aud"] = clientID
		accessClaims["scope"] = oauthTokenScope
	}
	access, err = jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString([]byte(accessSecret))
	if err != nil {
		return
	}

	refreshClaims := jwt.MapClaims{"sub": sub, "iat": now.Unix(), "exp": now.Add(refreshTTL).Unix()}
	if clientID != "" {
		refreshClaims["aud"] = clientID
		refreshClaims["scope"] = oauthTokenScope
	}
	refresh, err = jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString([]byte(refreshSecret))
	return
}

// tokenAudience extrai o claim aud de um JWT HS256 assinado por secret (mesmo
// molde do tokenSudoUntil). Vazio = token de sessão do painel, que nunca tem
// aud; não-vazio = token emitido pelo /oauth/token para aquele client_id.
// Token inválido também devolve vazio: quem decide sobre validade é o
// verifyToken do caller, aqui só lemos um claim de um token já confiável.
func tokenAudience(token, secret string) string {
	t, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok || t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(secret), nil
	})
	if err != nil || !t.Valid {
		return ""
	}
	claims, ok := t.Claims.(jwt.MapClaims)
	if !ok {
		return ""
	}
	switch aud := claims["aud"].(type) {
	case string:
		return aud
	case []any:
		if len(aud) > 0 {
			s, _ := aud[0].(string)
			return s
		}
	}
	return ""
}
