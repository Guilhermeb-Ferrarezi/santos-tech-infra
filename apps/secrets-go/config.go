package main

import (
	"log/slog"
	"os"
	"strings"
)

// Config reúne as variáveis de ambiente do secrets-go.
type Config struct {
	Port             string
	DatabaseURL      string
	JWTSecret        string
	CORSOrigins      []string
	GitHubToken      string
	ElasticsearchURL string
	StateFile        string
	// RedisURL é OPCIONAL — o domínio do secrets-go (scan de secrets vazados
	// no GitHub, herdado do dotfy-scanner) não usa Redis. Existe só para o
	// ipBanCheck (ban global por IP, mesmo namespace "global:ip-ban:<ip>"
	// usado pelos outros serviços Go do ecossistema) — ver ratelimit.go.
	// Ausente = ipBanCheck fica fail-open (sem bloqueio), igual ao
	// comportamento de payments-go quando s.rdb é nil.
	RedisURL string
}

func LoadConfig() Config {
	return Config{
		Port:             getEnv("PORT", "3338"),
		DatabaseURL:      mustEnv("DATABASE_URL"),
		JWTSecret:        mustEnv("JWT_SECRET"),
		CORSOrigins:      splitCSV(getEnv("CORS_ORIGIN", "")),
		GitHubToken:      mustEnv("GITHUB_TOKEN"),
		ElasticsearchURL: strings.TrimRight(mustEnv("ELASTICSEARCH_URL"), "/"),
		StateFile:        getEnv("STATE_FILE", "/data/state.json"),
		RedisURL:         getEnv("REDIS_URL", ""),
	}
}

func getEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		slog.Error("variável de ambiente obrigatória ausente", "key", k)
		os.Exit(1)
	}
	return v
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
