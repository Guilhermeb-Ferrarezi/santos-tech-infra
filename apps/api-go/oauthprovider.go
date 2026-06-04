package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"time"
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
