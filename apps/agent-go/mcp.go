package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// mcpConfig é o formato do arquivo --mcp-config do claude CLI.
type mcpConfig struct {
	MCPServers map[string]mcpServer `json:"mcpServers"`
}

type mcpServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// mcpConfigPath é o caminho do .mcp.json dentro do workdir da conversa.
func (s *Server) mcpConfigPath(conv *Conversation) string {
	return filepath.Join(conv.Workdir, ".mcp.json")
}

// writeMCPConfig gera o .mcp.json com o servidor MCP do GitHub (stdio).
// O comando é configurável via env (GITHUB_MCP_COMMAND/ARGS); o default usa npx.
func (s *Server) writeMCPConfig(conv *Conversation) error {
	if s.cfg.GithubToken == "" {
		// Sem token do GitHub: nenhum MCP configurado.
		return nil
	}
	command := getEnv("GITHUB_MCP_COMMAND", "npx")
	args := splitCSV(getEnv("GITHUB_MCP_ARGS", "-y,@modelcontextprotocol/server-github"))

	cfg := mcpConfig{MCPServers: map[string]mcpServer{
		"github": {
			Type:    "stdio",
			Command: command,
			Args:    args,
			Env: map[string]string{
				"GITHUB_PERSONAL_ACCESS_TOKEN": s.cfg.GithubToken,
				"GITHUB_TOKEN":                 s.cfg.GithubToken,
			},
		},
	}}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.mcpConfigPath(conv), data, 0o600)
}

// prepareWorkspace clona o repo (se informado) e escreve o .mcp.json no workdir.
// O clone vem primeiro: git clone exige diretório de destino vazio.
//
// writeMCPConfig SÓ roda quando há repo: o servidor MCP do GitHub sai armado com o
// GITHUB_TOKEN completo, e o campo "repo" é a única fronteira que o produto oferece
// pra dizer com o que aquela conversa deveria mexer. Escrever o .mcp.json incondicionalmente
// (como era antes) dava a QUALQUER conversa nova acesso ao GitHub inteiro do token, mesmo
// sem repositório declarado — contradizia o comentário de claudeArgs em session.go, que
// promete GITHUB_TOKEN "só quando há repo clonado" (verdade só pra env var do processo
// claude, não pro servidor MCP, que é um processo filho separado iniciado a partir deste
// arquivo).
func (s *Server) prepareWorkspace(ctx context.Context, conv *Conversation) error {
	if conv.Repo != nil && *conv.Repo != "" {
		if err := s.cloneRepo(ctx, conv); err != nil {
			return err
		}
		if err := s.writeMCPConfig(conv); err != nil {
			return fmt.Errorf("escrever .mcp.json: %w", err)
		}
	}
	return nil
}

// cloneRepo clona "org/repo" do GitHub no workdir. O token NUNCA entra no argv nem na
// URL: vai pelo GIT_ASKPASS (env), e a saída do git é higienizada antes de ir pro erro.
func (s *Server) cloneRepo(ctx context.Context, conv *Conversation) error {
	repo := strings.TrimSuffix(strings.TrimPrefix(*conv.Repo, "https://github.com/"), ".git")
	url := fmt.Sprintf("https://github.com/%s.git", repo)
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	if s.cfg.GithubToken != "" {
		askpass, cleanup, err := writeAskpass()
		if err != nil {
			return err
		}
		defer cleanup()
		url = fmt.Sprintf("https://x-access-token@github.com/%s.git", repo)
		env = append(env, "GIT_ASKPASS="+askpass, "GIT_ASKPASS_TOKEN="+s.cfg.GithubToken)
	}

	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "50", url, conv.Workdir)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		safe := scrubSecret(string(out), s.cfg.GithubToken)
		return fmt.Errorf("git clone falhou: %v: %s", err, strings.TrimSpace(safe))
	}
	return nil
}

// writeAskpass cria um helper temporário que imprime o token vindo do env (GIT_ASKPASS_TOKEN),
// mantendo o segredo fora do argv e da URL.
func writeAskpass() (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "askpass-*.sh")
	if err != nil {
		return "", nil, err
	}
	if _, err := f.WriteString("#!/bin/sh\nprintf '%s' \"$GIT_ASKPASS_TOKEN\"\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	_ = f.Chmod(0o700)
	_ = f.Close()
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}

// scrubSecret remove ocorrências do segredo de uma string (defesa em profundidade).
func scrubSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[REDACTED]")
}
