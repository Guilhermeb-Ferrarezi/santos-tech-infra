package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// authHTTP fala com o auth central. Não segue redirects (queremos ler o Set-Cookie).
var authHTTP = &http.Client{
	Timeout:       20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// POST /claude/auth/session — login para o app mobile.
// Faz o login no auth central server-side e devolve o access_token em JSON, já que
// o auth só entrega o token via cookie httpOnly (que o mobile não lê de forma confiável).
// Suporta o 2º passo de MFA via {challenge, code}.
func (s *Server) handleMobileSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Identifier string `json:"identifier"`
		Password   string `json:"password"`
		Challenge  string `json:"challenge"`
		Code       string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", "corpo inválido"))
		return
	}

	var path string
	var payload any
	if body.Challenge != "" && body.Code != "" {
		path, payload = "/auth/mfa/verify", map[string]string{"challenge": body.Challenge, "code": body.Code}
	} else {
		path, payload = "/auth/login", map[string]string{"identifier": body.Identifier, "password": body.Password}
	}

	pb, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, s.cfg.AuthAPIURL+path, bytes.NewReader(pb))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agent-go/mobile-session")
	resp, err := authHTTP.Do(req)
	if err != nil {
		writeErr(w, appErr(http.StatusBadGateway, "AUTH_UNREACHABLE", "Falha ao contatar o auth: "+err.Error()))
		return
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var bodyMap map[string]any
	_ = json.Unmarshal(raw, &bodyMap)

	// MFA exigido: repassa o desafio para o app pedir o código.
	if mfa, _ := bodyMap["mfaRequired"].(bool); mfa {
		writeJSON(w, http.StatusOK, map[string]any{"mfaRequired": true, "challenge": bodyMap["challenge"]})
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, resp.StatusCode, bodyMap) // repassa o erro do auth ({code,message})
		return
	}

	token := extractCookie(resp.Header, "access_token")
	if token == "" {
		writeErr(w, appErr(http.StatusBadGateway, "NO_TOKEN", "Auth não retornou token"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accessToken": token, "user": bodyMap["user"]})
}

func extractCookie(h http.Header, name string) string {
	for _, c := range (&http.Response{Header: h}).Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}
