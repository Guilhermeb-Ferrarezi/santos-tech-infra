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

// CoolifyClient — cliente leve da API da Coolify usado pelos comandos de operação
// via WhatsApp (/status, /redeploy, /logs). Lê COOLIFY_API_URL e COOLIFY_API_TOKEN
// de env; quando ausentes, Enabled()=false e os comandos degradam com graça
// respondendo "Coolify não configurada" (não derruba o boot).
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

// CoolifyApp — resumo de uma aplicação da Coolify.
type CoolifyApp struct {
	Name   string `json:"name"`
	UUID   string `json:"uuid"`
	Status string `json:"status"` // ex.: "running:healthy", "exited", "stopped"
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

// Applications lista as aplicações da Coolify (GET /api/v1/applications).
func (c *CoolifyClient) Applications(ctx context.Context) ([]CoolifyApp, error) {
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/applications")
	if err != nil {
		return nil, err
	}
	var apps []CoolifyApp
	if err := json.Unmarshal(raw, &apps); err != nil {
		return nil, fmt.Errorf("coolify: applications parse: %w", err)
	}
	return apps, nil
}

// Deploy dispara um deploy da aplicação (POST /api/v1/deploy?uuid=X&force=true).
func (c *CoolifyClient) Deploy(ctx context.Context, uuid string) error {
	_, err := c.do(ctx, http.MethodPost, "/api/v1/deploy?uuid="+uuid+"&force=true")
	return err
}

// DeploymentLogs retorna as últimas linhas do último deployment de uma aplicação.
// GET /api/v1/deployments/{uuid} — o campo "logs" é uma string JSON de objetos
// com "output". Retorna no máximo `maxLines` linhas (as mais recentes).
func (c *CoolifyClient) DeploymentLogs(ctx context.Context, uuid string, maxLines int) (string, error) {
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/deployments/"+uuid)
	if err != nil {
		return "", err
	}
	var dep struct {
		Logs string `json:"logs"`
	}
	if err := json.Unmarshal(raw, &dep); err != nil {
		return "", fmt.Errorf("coolify: deployment parse: %w", err)
	}
	if dep.Logs == "" {
		return "", nil
	}

	// O campo "logs" é uma string JSON de objetos {output,...}.
	var entries []struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(dep.Logs), &entries); err != nil {
		// Formato inesperado: devolve o texto cru, truncado.
		return tailLines(dep.Logs, maxLines), nil
	}

	var lines []string
	for _, e := range entries {
		for _, ln := range strings.Split(strings.TrimRight(e.Output, "\n"), "\n") {
			if strings.TrimSpace(ln) != "" {
				lines = append(lines, ln)
			}
		}
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n"), nil
}

// ResolveApp encontra uma aplicação por nome (match case-insensitive por
// substring). Retorna (app, candidatos, erro):
//   - match exato (case-insensitive) tem prioridade;
//   - 1 candidato por substring → resolve;
//   - múltiplos candidatos → app=nil, candidatos preenchido (ambíguo);
//   - nenhum → app=nil, candidatos vazio.
func ResolveApp(apps []CoolifyApp, query string) (*CoolifyApp, []CoolifyApp) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, nil
	}
	// Match exato primeiro.
	for i := range apps {
		if strings.EqualFold(apps[i].Name, q) {
			return &apps[i], nil
		}
	}
	var matches []CoolifyApp
	for i := range apps {
		if strings.Contains(strings.ToLower(apps[i].Name), q) {
			matches = append(matches, apps[i])
		}
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &matches[0], nil
	default:
		return nil, matches
	}
}

// coolifyAppUp diz se o status reportado pela Coolify indica app de pé.
func coolifyAppUp(status string) bool {
	return strings.HasPrefix(strings.ToLower(status), "running")
}

// tailLines devolve as últimas n linhas de um texto.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
