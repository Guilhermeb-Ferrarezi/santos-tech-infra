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

// claudeEnv monta o ambiente do processo do Claude. Como ele roda com
// --dangerously-skip-permissions sobre conteúdo semi-confiável (logs de build,
// mensagem de commit), aplicamos uma allowlist: herda o ambiente do fixer MENOS
// os segredos sensíveis, e injeta só o OAuth do Claude. O git (clone/commit/push)
// é todo do orquestrador (gitops.go, via askpass), então o filho não precisa —
// nem deve ver — o GITHUB_TOKEN. Mesmo sob prompt-injection, não há credencial
// do GitHub/Coolify/Evolution no ambiente do filho para exfiltrar.
func claudeEnv(cfg Config) []string {
	blocked := map[string]bool{
		"GITHUB_TOKEN":                 true,
		"GITHUB_PERSONAL_ACCESS_TOKEN": true,
		"COOLIFY_API_TOKEN":            true,
		"COOLIFY_WEBHOOK_SECRET":       true,
		"EVOLUTION_API_KEY":            true,
		"REDIS_URL":                    true,
		"GH_APP_PRIVATE_KEY":           true,
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		// Remove segredos do fixer e qualquer CLAUDE_CODE_OAUTH herdado (reinjetado abaixo).
		if blocked[k] || strings.HasPrefix(k, "CLAUDE_CODE_OAUTH") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "CLAUDE_CODE_OAUTH_TOKEN="+cfg.ClaudeOAuth)
}

// claudeBaseArgs monta os flags do `claude` em modo headless (-p).
func claudeBaseArgs(cfg Config, workdir string) []string {
	return []string{
		"-p", "--output-format", "stream-json", "--verbose",
		"--model", cfg.ClaudeModel,
		"--dangerously-skip-permissions", "--add-dir", workdir,
	}
}

// dockerRunArgs monta o `docker run` que executa o Claude num container efêmero
// e descartável (Task 11): isolado do processo do fixer, com APENAS o OAuth do
// Claude no ambiente (nenhum segredo do fixer), o workspace montado por volume,
// e rede opcionalmente restrita. O git (clone/push) continua no orquestrador.
func dockerRunArgs(cfg Config, workdir string) []string {
	args := []string{
		"run", "--rm", "-i",
		"-v", cfg.WorkspaceVolume + ":" + cfg.WorkspaceRoot,
		"-w", workdir,
		"-e", "CLAUDE_CODE_OAUTH_TOKEN=" + cfg.ClaudeOAuth,
	}
	if cfg.ClaudeNetwork != "" {
		args = append(args, "--network", cfg.ClaudeNetwork)
	}
	args = append(args, "--entrypoint", cfg.ClaudeBin, cfg.RunnerImage)
	return append(args, claudeBaseArgs(cfg, workdir)...)
}

// claudeCommand devolve o *exec.Cmd para um turno do Claude, escolhendo entre
// container efêmero (Sandbox=docker) e exec direto no processo do fixer.
func claudeCommand(ctx context.Context, cfg Config, workdir string) *exec.Cmd {
	if cfg.Sandbox == "docker" {
		// O isolamento é o container: ele só recebe o que passamos via -e.
		cmd := exec.CommandContext(ctx, "docker", dockerRunArgs(cfg, workdir)...)
		cmd.Env = os.Environ() // ambiente do CLIENTE docker (não entra no container)
		return cmd
	}
	cmd := exec.CommandContext(ctx, cfg.ClaudeBin, claudeBaseArgs(cfg, workdir)...)
	cmd.Dir = workdir
	cmd.Env = claudeEnv(cfg) // allowlist: sem segredos do fixer
	return cmd
}

func runClaudeFix(ctx context.Context, cfg Config, workdir, buildLogs, commitMsg string) (string, error) {
	prompt := fmt.Sprintf(fixPromptTmpl, commitMsg, tailLines(buildLogs, 200))
	cmd := claudeCommand(ctx, cfg, workdir)
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
