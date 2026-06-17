package main

import (
	"strings"
	"testing"
)

func TestClaudeEnvBlocksSecrets(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_segredo")
	t.Setenv("COOLIFY_API_TOKEN", "cool_segredo")
	t.Setenv("EVOLUTION_API_KEY", "evo_segredo")
	t.Setenv("PATH", "/usr/bin")

	env := claudeEnv(Config{ClaudeOAuth: "oauth_xyz"})
	joined := strings.Join(env, "\n")

	for _, secret := range []string{"ghp_segredo", "cool_segredo", "evo_segredo"} {
		if strings.Contains(joined, secret) {
			t.Errorf("segredo vazou para o env do Claude: %q", secret)
		}
	}
	if !strings.Contains(joined, "CLAUDE_CODE_OAUTH_TOKEN=oauth_xyz") {
		t.Error("OAuth do Claude deveria estar no env")
	}
	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Error("PATH (não-sensível) deveria ser herdado")
	}
}

func TestParseClaudeResult(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"olhando"}]}}`,
		`{"type":"result","subtype":"success","result":"Corrigi o Dockerfile: faltava COPY do go.sum."}`,
	}
	if got := parseClaudeResult(lines); got != "Corrigi o Dockerfile: faltava COPY do go.sum." {
		t.Fatalf("got %q", got)
	}
}

func TestParseClaudeResultEmpty(t *testing.T) {
	if got := parseClaudeResult([]string{`{"type":"system"}`}); got != "" {
		t.Fatalf("got %q", got)
	}
}
