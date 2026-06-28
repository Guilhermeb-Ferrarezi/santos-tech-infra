package main

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config carrega o ambiente do serviço. Espelha o padrão de apps/api-go/config.go,
// adicionando o que é específico do orquestrador do Claude.
type Config struct {
	Port        string
	DatabaseURL string
	RedisURL    string

	// Auth dos usuários (mesmo JWT_SECRET do auth central — tokens compatíveis).
	JWTSecret   string
	CORSOrigins []string

	// URL da API de auth central (para o endpoint de login do app mobile).
	AuthAPIURL string

	// Cifragem do token OAuth do Claude em repouso (AES-256-GCM → 32 bytes).
	EncryptionKey string

	// Runtime do Claude.
	WorkspaceRoot string // onde os repos são clonados (ex: /data/workspaces)
	ClaudeBin     string // caminho do binário "claude"
	DefaultModel  string

	// Tokens de infra injetados no ambiente do agente (ele usa via bash/curl).
	EasypanelURL    string
	EasypanelToken  string
	CloudflareToken string
	GithubToken     string // usado pelo MCP do GitHub

	// Secret de serviço: permite que serviços internos (ex: bot-atendimento) chamem
	// /claude/generate sem JWT de usuário. Ausente = desabilitado.
	InternalSecret string

	// Máximo de sessões vivas simultâneas (processos de longa duração por conversa).
	MaxLive int

	// Tempo ocioso máximo antes de hibernar uma sessão viva sem WS conectado.
	IdleTTL time.Duration

	// Teto de duração de um único turno no motor vivo. Zero → usa a constante turnTimeout (8m).
	TurnTimeout time.Duration

	Production bool
}

func LoadConfig() Config {
	return Config{
		Port:          getEnv("PORT", "3334"),
		DatabaseURL:   mustEnv("DATABASE_URL"),
		RedisURL:      mustEnv("REDIS_URL"),
		JWTSecret:     mustEnv("JWT_SECRET"),
		CORSOrigins:   splitCSV(getEnv("CORS_ORIGIN", "")),
		AuthAPIURL:    strings.TrimRight(getEnv("AUTH_API_URL", "https://api.santos-tech.com"), "/"),
		EncryptionKey: mustEnv("ENCRYPTION_KEY"),

		WorkspaceRoot: getEnv("CLAUDE_WORKSPACE_ROOT", "/data/workspaces"),
		ClaudeBin:     getEnv("CLAUDE_BIN", "claude"),
		DefaultModel:  getEnv("CLAUDE_DEFAULT_MODEL", "sonnet"),

		EasypanelURL:    strings.TrimRight(getEnv("EASYPANEL_URL", "https://cloud.santos-tech.com"), "/"),
		EasypanelToken:  getEnv("EASYPANEL_TOKEN", ""),
		CloudflareToken: getEnv("CLOUDFLARE_API_TOKEN", ""),
		GithubToken:     getEnv("GITHUB_TOKEN", ""),

		InternalSecret: getEnv("INTERNAL_SECRET", ""),

		MaxLive:     envInt("CLAUDE_MAX_LIVE", 4),
		IdleTTL:     envDuration("CLAUDE_IDLE_TTL", 15*time.Minute),
		TurnTimeout: envDuration("CLAUDE_TURN_TIMEOUT", turnTimeout),

		Production: getEnv("NODE_ENV", "development") == "production",
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("variável de ambiente obrigatória ausente", "key", key)
		os.Exit(1)
	}
	return v
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
