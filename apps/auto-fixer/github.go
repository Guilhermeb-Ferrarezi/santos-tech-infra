package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// GitHubApp gera installation tokens efêmeros escopados a um repositório. Cobre
// repos em owners diferentes (conta pessoal + org) via uma installation por owner,
// descoberta sob demanda a partir do (owner, repo). Quando não configurado,
// Enabled()=false e o orquestrador cai para o PAT (GITHUB_TOKEN), logando o downgrade.
type GitHubApp struct {
	appID string
	key   *rsa.PrivateKey
	http  *http.Client
}

// NewGitHubApp constrói o cliente. Retorna (nil, nil) se appID/keyPEM vazios
// (App não configurado — degrada para PAT). Erro só se a chave for inválida.
func NewGitHubApp(appID, keyPEM string) (*GitHubApp, error) {
	if appID == "" || strings.TrimSpace(keyPEM) == "" {
		return nil, nil
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("github app: chave privada inválida: %w", err)
	}
	return &GitHubApp{appID: appID, key: key, http: &http.Client{Timeout: 20 * time.Second}}, nil
}

func (g *GitHubApp) Enabled() bool { return g != nil && g.key != nil }

// appJWT monta o JWT do App (RS256), válido por ~9 min. now é injetável p/ teste.
func (g *GitHubApp) appJWT(now time.Time) (string, error) {
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer:    g.appID,
		IssuedAt:  jwt.NewNumericDate(now.Add(-30 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(9 * time.Minute)),
	})
	return tok.SignedString(g.key)
}

func (g *GitHubApp) get(ctx context.Context, jwtTok, url string, out any) error {
	return g.call(ctx, http.MethodGet, jwtTok, url, nil, out)
}

func (g *GitHubApp) call(ctx context.Context, method, bearer, url string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github %s %s: status %d: %s", method, url, resp.StatusCode, string(raw))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// installationID descobre a installation que cobre o repo (GET /repos/{o}/{r}/installation).
func (g *GitHubApp) installationID(ctx context.Context, jwtTok, owner, repo string) (int64, error) {
	var inst struct {
		ID int64 `json:"id"`
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/installation", owner, repo)
	if err := g.get(ctx, jwtTok, url, &inst); err != nil {
		return 0, err
	}
	if inst.ID == 0 {
		return 0, fmt.Errorf("github app: sem installation para %s/%s", owner, repo)
	}
	return inst.ID, nil
}

// RepoToken devolve um installation token efêmero (TTL ~1h) escopado só a este repo.
func (g *GitHubApp) RepoToken(ctx context.Context, now time.Time, owner, repo string) (string, error) {
	jwtTok, err := g.appJWT(now)
	if err != nil {
		return "", err
	}
	id, err := g.installationID(ctx, jwtTok, owner, repo)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{
		"repositories": []string{repo},
		"permissions":  map[string]string{"contents": "write"},
	})
	var tr struct {
		Token string `json:"token"`
	}
	url := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", id)
	if err := g.call(ctx, http.MethodPost, jwtTok, url, bytes.NewReader(body), &tr); err != nil {
		return "", err
	}
	if tr.Token == "" {
		return "", fmt.Errorf("github app: token vazio na resposta")
	}
	return tr.Token, nil
}

// repoCloneURL normaliza o campo da Coolify (que vem como "owner/repo") para uma
// URL https clonável. Aceita também URLs já completas (https:// ou git@).
func repoCloneURL(gitRepo string) string {
	s := strings.TrimSpace(gitRepo)
	if strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "git@") {
		return s
	}
	s = strings.TrimSuffix(strings.Trim(s, "/"), ".git")
	return "https://github.com/" + s + ".git"
}

// parseRepoURL extrai (owner, repo) de uma URL de repositório GitHub.
// Aceita https://github.com/owner/repo(.git) e git@github.com:owner/repo(.git).
func parseRepoURL(u string) (owner, repo string, err error) {
	s := strings.TrimSpace(u)
	s = strings.TrimSuffix(s, ".git")
	switch {
	case strings.HasPrefix(s, "https://"):
		s = strings.TrimPrefix(s, "https://")
		if i := strings.IndexByte(s, '/'); i >= 0 {
			s = s[i+1:] // descarta o host (github.com)
		}
	case strings.HasPrefix(s, "git@"):
		if i := strings.IndexByte(s, ':'); i >= 0 {
			s = s[i+1:] // descarta git@github.com:
		}
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("url de repo inválida: %q", u)
	}
	return parts[0], parts[1], nil
}
