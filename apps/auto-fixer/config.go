package main

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Config reúne todas as variáveis de ambiente do auto-fixer.
type Config struct {
	Port            string // PORT (default 3340)
	WebhookSecret   string // COOLIFY_WEBHOOK_SECRET (token na query)
	RedisURL        string // REDIS_URL (sidecar próprio)
	CoolifyAPIURL   string // COOLIFY_API_URL
	CoolifyAPIToken string // COOLIFY_API_TOKEN
	EvolutionURL    string // EVOLUTION_API_URL
	EvolutionKey    string // EVOLUTION_API_KEY
	EvolutionInst   string // EVOLUTION_INSTANCE
	GroupJID        string // NOTIF_GROUP_JID (ex: 1203...@g.us)
	ClaudeBin       string // CLAUDE_BIN (default "claude")
	ClaudeModel     string // CLAUDE_DEFAULT_MODEL (default "sonnet")
	ClaudeOAuth     string // CLAUDE_CODE_OAUTH_TOKEN
	GithubToken     string // GITHUB_TOKEN (fallback/PAT — só askpass do orquestrador: clone/push; nunca vai ao processo do Claude)
	GHAppID         string // GH_APP_ID (preferencial: token efêmero escopado por repo)
	GHAppKey        string // GH_APP_PRIVATE_KEY (PEM; aceita \n literais)
	WorkspaceRoot   string // WORKSPACE_ROOT (default /data/workspaces)
	// AllowedRepos é a allow-list de repositórios que o auto-fix pode clonar,
	// em "org/repo" minúsculo (env ALLOWED_REPOS, CSV). Sem ela, o repo vinha
	// cru do webhook/Coolify e era clonado com o token da organização.
	AllowedRepos   []string // ALLOWED_REPOS
	MaxFixAttempts int      // MAX_FIX_ATTEMPTS (default 2)
	RebuildTimeout int      // REBUILD_TIMEOUT_MIN (default 15)
	// Sandbox do Claude (Task 11). Sandbox="docker" roda o Claude num container
	// `docker run --rm` descartável por incidente (isolado do processo do fixer);
	// vazio = exec direto no processo do fixer.
	Sandbox         string // CLAUDE_SANDBOX ("docker" | "")
	RunnerImage     string // CLAUDE_RUNNER_IMAGE (imagem com o claude CLI; obrigatória se Sandbox=docker)
	WorkspaceVolume string // WORKSPACE_VOLUME (named volume montado no runner, ex: auto-fixer_workspaces)
	ClaudeNetwork   string // CLAUDE_DOCKER_NETWORK (rede do runner; vazio = default do docker)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// splitCSV quebra uma lista CSV em itens normalizados (trim + minúsculas),
// descartando vazios.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if v := strings.ToLower(strings.TrimSpace(p)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func atoiDef(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}

// loadConfig lê a configuração do ambiente. Falha cedo e claro se faltar um
// campo crítico (sem ele o serviço não funciona).
func loadConfig() (Config, error) {
	c := Config{
		Port:            env("PORT", "3340"),
		WebhookSecret:   os.Getenv("COOLIFY_WEBHOOK_SECRET"),
		RedisURL:        env("REDIS_URL", "redis://localhost:6379/0"),
		CoolifyAPIURL:   os.Getenv("COOLIFY_API_URL"),
		CoolifyAPIToken: os.Getenv("COOLIFY_API_TOKEN"),
		EvolutionURL:    os.Getenv("EVOLUTION_API_URL"),
		EvolutionKey:    os.Getenv("EVOLUTION_API_KEY"),
		EvolutionInst:   os.Getenv("EVOLUTION_INSTANCE"),
		GroupJID:        os.Getenv("NOTIF_GROUP_JID"),
		ClaudeBin:       env("CLAUDE_BIN", "claude"),
		ClaudeModel:     env("CLAUDE_DEFAULT_MODEL", "sonnet"),
		ClaudeOAuth:     os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"),
		GithubToken:     os.Getenv("GITHUB_TOKEN"),
		GHAppID:         os.Getenv("GH_APP_ID"),
		GHAppKey:        strings.ReplaceAll(os.Getenv("GH_APP_PRIVATE_KEY"), "\\n", "\n"),
		WorkspaceRoot:   env("WORKSPACE_ROOT", "/data/workspaces"),
		AllowedRepos:    splitCSV(os.Getenv("ALLOWED_REPOS")),
		MaxFixAttempts:  atoiDef(os.Getenv("MAX_FIX_ATTEMPTS"), 2),
		RebuildTimeout:  atoiDef(os.Getenv("REBUILD_TIMEOUT_MIN"), 15),
		Sandbox:         os.Getenv("CLAUDE_SANDBOX"),
		RunnerImage:     os.Getenv("CLAUDE_RUNNER_IMAGE"),
		WorkspaceVolume: os.Getenv("WORKSPACE_VOLUME"),
		ClaudeNetwork:   os.Getenv("CLAUDE_DOCKER_NETWORK"),
	}
	// Fail-closed: sem allow-list explícita o auto-fix não clona nada.
	if len(c.AllowedRepos) == 0 {
		return c, fmt.Errorf(`config: ALLOWED_REPOS obrigatório (CSV "org/repo") — allow-list dos repositórios que o auto-fix pode clonar`)
	}
	if c.Sandbox == "docker" && (c.RunnerImage == "" || c.WorkspaceVolume == "") {
		return c, fmt.Errorf("config: CLAUDE_SANDBOX=docker exige CLAUDE_RUNNER_IMAGE e WORKSPACE_VOLUME")
	}
	// Auth do GitHub: precisa de pelo menos um caminho — GitHub App (preferencial)
	// ou PAT (fallback). Não exige os dois.
	if c.GithubToken == "" && (c.GHAppID == "" || c.GHAppKey == "") {
		return c, fmt.Errorf("config: defina GITHUB_TOKEN ou (GH_APP_ID + GH_APP_PRIVATE_KEY)")
	}
	for k, v := range map[string]string{
		"REDIS_URL": c.RedisURL, "COOLIFY_API_URL": c.CoolifyAPIURL,
		"COOLIFY_API_TOKEN": c.CoolifyAPIToken, "EVOLUTION_API_URL": c.EvolutionURL,
		"EVOLUTION_API_KEY": c.EvolutionKey, "EVOLUTION_INSTANCE": c.EvolutionInst,
		"NOTIF_GROUP_JID": c.GroupJID, "CLAUDE_CODE_OAUTH_TOKEN": c.ClaudeOAuth,
		// Obrigatório: sem ele o webhook aceitava POST anônimo e qualquer um
		// disparava o ciclo de auto-fix (clone + Claude + push na branch de deploy).
		"COOLIFY_WEBHOOK_SECRET": c.WebhookSecret,
	} {
		if v == "" {
			return c, fmt.Errorf("config: %s obrigatório", k)
		}
	}
	return c, nil
}

// repoAllowed diz se o repositório indicado pelo webhook/Coolify está na
// allow-list. Aceita "owner/repo" ou URL completa e compara a identidade
// canônica (owner/repo minúsculo), não a string crua.
//
// SEGURANÇA: sem isso, quem controlasse o payload do webhook fazia o auto-fixer
// clonar QUALQUER repositório usando o token da organização (GitHub App ou PAT).
func (c Config) repoAllowed(repo string) bool {
	owner, name, err := parseRepoURL(repoCloneURL(repo))
	if err != nil {
		return false
	}
	key := strings.ToLower(owner + "/" + name)
	return slices.Contains(c.AllowedRepos, key)
}
