package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// parseClaudeResult extrai o texto do evento final "result" do stream-json.
func parseClaudeResult(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		var ev map[string]any
		if json.Unmarshal([]byte(lines[i]), &ev) != nil {
			continue
		}
		if ev["type"] == "result" {
			if s, ok := ev["result"].(string); ok {
				return s
			}
		}
	}
	return ""
}

const fixPromptTmpl = `O build/deploy deste repositório FALHOU. Sua tarefa: descobrir a causa, corrigir o código e deixar o build passando. NÃO faça mudanças não relacionadas.

Commit que quebrou: %s

--- LOGS DO BUILD (fim do log é o mais relevante) ---
%s
--- FIM DOS LOGS ---

Conserte a causa raiz. Quando terminar, descreva em 1-2 frases o que era o problema e o que você mudou. Não faça commit nem push — a orquestração cuida disso.`

func runClaudeFix(ctx context.Context, cfg Config, workdir, buildLogs, commitMsg string) (string, error) {
	prompt := fmt.Sprintf(fixPromptTmpl, commitMsg, tailLines(buildLogs, 200))
	args := []string{
		"-p", "--output-format", "stream-json", "--verbose",
		"--model", cfg.ClaudeModel,
		"--dangerously-skip-permissions", "--add-dir", workdir,
	}
	cmd := exec.CommandContext(ctx, cfg.ClaudeBin, args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(),
		"CLAUDE_CODE_OAUTH_TOKEN="+cfg.ClaudeOAuth,
		"GITHUB_TOKEN="+cfg.GithubToken,
		"GITHUB_PERSONAL_ACCESS_TOKEN="+cfg.GithubToken,
	)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var lines []string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		if t := sc.Text(); t != "" {
			lines = append(lines, t)
		}
	}
	if err := cmd.Wait(); err != nil {
		return parseClaudeResult(lines), fmt.Errorf("claude saiu com erro: %w", err)
	}
	return parseClaudeResult(lines), nil
}
