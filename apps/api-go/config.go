package main

import (
	"log/slog"
	"os"
	"strings"
)

type Config struct {
	Port               string
	DatabaseURL        string
	RedisURL           string
	JWTSecret          string
	JWTRefreshSecret   string
	CookieDomain       string
	CORSOrigins        []string
	AuthWebOrigin      string
	GoogleClientID     string
	GoogleClientSecret string
	GoogleCallbackURL  string
	EmailAPIURL        string // ex: https://mails.santos-tech.com/api
	EmailAPIKey        string
	Production         bool
}

func LoadConfig() Config {
	c := Config{
		Port:               getEnv("PORT", "3333"),
		DatabaseURL:        mustEnv("DATABASE_URL"),
		RedisURL:           mustEnv("REDIS_URL"),
		JWTSecret:          mustEnv("JWT_SECRET"),
		JWTRefreshSecret:   mustEnv("JWT_REFRESH_SECRET"),
		CookieDomain:       getEnv("COOKIE_DOMAIN", ""),
		CORSOrigins:        splitCSV(getEnv("CORS_ORIGIN", "")),
		AuthWebOrigin:      getEnv("AUTH_WEB_ORIGIN", ""),
		GoogleClientID:     getEnv("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret: getEnv("GOOGLE_CLIENT_SECRET", ""),
		GoogleCallbackURL:  getEnv("GOOGLE_CALLBACK_URL", ""),
		EmailAPIURL:        strings.TrimRight(getEnv("EMAIL_API_URL", "https://mails.santos-tech.com/api"), "/"),
		EmailAPIKey:        mustEnv("EMAIL_API_KEY"),
		Production:         getEnv("NODE_ENV", "development") == "production",
	}
	if c.AuthWebOrigin == "" && len(c.CORSOrigins) > 0 {
		c.AuthWebOrigin = c.CORSOrigins[0]
	}
	return c
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
