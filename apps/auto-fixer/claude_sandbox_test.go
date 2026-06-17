package main

import (
	"strings"
	"testing"
)

func argsContain(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestDockerRunArgs(t *testing.T) {
	cfg := Config{
		ClaudeBin: "claude", ClaudeModel: "sonnet", ClaudeOAuth: "oauth_xyz",
		WorkspaceRoot: "/data/workspaces", WorkspaceVolume: "af_workspaces",
		RunnerImage: "auto-fixer:latest", ClaudeNetwork: "isolated",
	}
	args := dockerRunArgs(cfg, "/data/workspaces/bot-go-123")
	joined := strings.Join(args, " ")

	if args[0] != "run" || !argsContain(args, "--rm") || !argsContain(args, "-i") {
		t.Fatalf("faltam flags base: %v", args)
	}
	if !strings.Contains(joined, "af_workspaces:/data/workspaces") {
		t.Errorf("volume errado: %s", joined)
	}
	if !argsContain(args, "/data/workspaces/bot-go-123") {
		t.Error("workdir ausente")
	}
	// OAuth presente; NENHUM segredo do fixer (o container só recebe o -e que passamos).
	if !strings.Contains(joined, "CLAUDE_CODE_OAUTH_TOKEN=oauth_xyz") {
		t.Error("OAuth ausente")
	}
	if strings.Contains(joined, "GITHUB") {
		t.Errorf("não deveria haver GITHUB no docker run: %s", joined)
	}
	if !strings.Contains(joined, "--network isolated") {
		t.Error("rede ausente")
	}
	if !strings.Contains(joined, "--entrypoint claude auto-fixer:latest") {
		t.Errorf("entrypoint/imagem errado: %s", joined)
	}
	// O claude (modo -p) deve vir DEPOIS da imagem.
	if !strings.Contains(joined, "auto-fixer:latest -p --output-format stream-json") {
		t.Errorf("args do claude fora de ordem: %s", joined)
	}
}

func TestDockerRunArgsNoNetwork(t *testing.T) {
	cfg := Config{ClaudeBin: "claude", ClaudeModel: "sonnet", WorkspaceRoot: "/w", WorkspaceVolume: "v", RunnerImage: "img"}
	if argsContain(dockerRunArgs(cfg, "/w/x"), "--network") {
		t.Error("não deveria ter --network quando vazio")
	}
}
