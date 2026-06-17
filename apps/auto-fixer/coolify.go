package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CoolifyClient — cliente leve da API da Coolify usado pelo auto-fixer para
// enriquecer o incidente com o commit que quebrou, puxar o log de BUILD do
// deployment e descobrir o repositório/branch git da aplicação. Lê url/token
// da config; sem eles, Enabled()=false e os métodos degradam respondendo
// "coolify: não configurada" (não derruba o boot).
type CoolifyClient struct {
	url   string
	token string
	http  *http.Client
}

// NewCoolifyClient cria o cliente. Enabled()=false se faltar url/token.
func NewCoolifyClient(url, token string) *CoolifyClient {
	return &CoolifyClient{
		url:   strings.TrimRight(url, "/"),
		token: token,
		http:  &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *CoolifyClient) Enabled() bool {
	return c != nil && c.url != "" && c.token != ""
}

func (c *CoolifyClient) do(ctx context.Context, method, path string) ([]byte, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("coolify: não configurada")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("coolify: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("coolify: %s %s status %d", method, path, resp.StatusCode)
	}
	return raw, nil
}

// DeploymentInfo retorna o commit (sha) e a mensagem do commit de um deployment.
// O webhook de notificação não traz isso, mas a API do deployment sim.
func (c *CoolifyClient) DeploymentInfo(ctx context.Context, uuid string) (commit, message string, err error) {
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/deployments/"+uuid)
	if err != nil {
		return "", "", err
	}
	var d struct {
		Commit        string `json:"commit"`
		CommitMessage string `json:"commit_message"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", "", err
	}
	return d.Commit, d.CommitMessage, nil
}

// BuildLogs extrai o log de BUILD do deployment. O campo "logs" vem como string
// JSON de objetos {output:...}; concatena os outputs em texto plano.
func (c *CoolifyClient) BuildLogs(ctx context.Context, deploymentUUID string) (string, error) {
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/deployments/"+deploymentUUID)
	if err != nil {
		return "", err
	}
	var d struct {
		Logs string `json:"logs"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", err
	}
	if d.Logs == "" {
		return "", nil
	}
	var lines []struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(d.Logs), &lines); err != nil {
		// Formato inesperado: devolve cru (melhor que nada pro Claude ler).
		return d.Logs, nil
	}
	var out []string
	for _, l := range lines {
		out = append(out, l.Output)
	}
	return strings.Join(out, "\n"), nil
}

// AppRepo retorna o repositório git e a branch configurados na aplicação.
func (c *CoolifyClient) AppRepo(ctx context.Context, appUUID string) (repo, branch string, err error) {
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/applications/"+appUUID)
	if err != nil {
		return "", "", err
	}
	var a struct {
		GitRepository string `json:"git_repository"`
		GitBranch     string `json:"git_branch"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", "", err
	}
	return a.GitRepository, a.GitBranch, nil
}
