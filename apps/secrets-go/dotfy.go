package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

const dotfyAuthMeURL = "https://app.dotfy.com.br/api/auth/me"

// DotfyVerifier confirma se uma chave candidata ainda está ativa chamando o
// healthcheck oficial da Dotfy (GET /api/auth/me): 200 = ativa, 401 = inválida
// ou revogada. Só deve ser chamado pra chaves achadas em repos da allowlist
// (ownRepoOwners) — ver isOwnRepo em verify.go.
type DotfyVerifier struct {
	http *http.Client
}

func NewDotfyVerifier() *DotfyVerifier {
	return &DotfyVerifier{http: &http.Client{Timeout: 10 * time.Second}}
}

type dotfyAuthMeResponse struct {
	Authenticated bool `json:"authenticated"`
}

// CheckKey retorna (checked, active). checked=false significa que não
// conseguimos confirmar nada (erro de rede/timeout) — isso NÃO deve ser
// interpretado como "chave inválida", só como "não testamos".
func (d *DotfyVerifier) CheckKey(ctx context.Context, key string) (checked bool, active bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dotfyAuthMeURL, nil)
	if err != nil {
		return false, false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "secrets-go")

	resp, err := d.http.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var out dotfyAuthMeResponse
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return true, out.Authenticated
	case http.StatusUnauthorized, http.StatusForbidden:
		return true, false
	default:
		return false, false
	}
}
