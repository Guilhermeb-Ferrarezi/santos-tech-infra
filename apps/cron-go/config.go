package main

import (
	"os"
	"strings"
)

type Config struct {
	Port          string
	DatabaseURL   string
	Production    bool
	ServicePAT    string   // Bearer usado pelo dispatcher ao chamar alvos
	AllowRawHTTP  bool     // habilita action_kind="http" com URL livre
	HostAllowlist []string // sufixos de host permitidos (ex.: ".santos-tech.com")
	AuthMeURL     string   // URL do /auth/me para validar sessão (guard de admin)
	CORSOrigins   []string // origens permitidas no CORS (CSV em CORS_ORIGIN)
}

func LoadConfig() Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3337"
	}
	allow := os.Getenv("CRON_HOST_ALLOWLIST")
	if allow == "" {
		allow = ".santos-tech.com"
	}
	parts := strings.Split(allow, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	authMeURL := os.Getenv("AUTH_ME_URL")
	if authMeURL == "" {
		authMeURL = "https://api.santos-tech.com/auth/me"
	}
	var cors []string
	for _, o := range strings.Split(os.Getenv("CORS_ORIGIN"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			cors = append(cors, o)
		}
	}
	return Config{
		Port:          port,
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		Production:    os.Getenv("NODE_ENV") == "production",
		ServicePAT:    os.Getenv("CRON_SERVICE_PAT"),
		AllowRawHTTP:  os.Getenv("CRON_ALLOW_RAW_HTTP") == "1",
		HostAllowlist: parts,
		AuthMeURL:     authMeURL,
		CORSOrigins:   cors,
	}
}
