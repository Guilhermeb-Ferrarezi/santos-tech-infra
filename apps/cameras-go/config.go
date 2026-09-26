package main

import (
	"fmt"
	"os"
)

type Config struct {
	Port          string
	DatabaseURL   string
	JWTSecret     string
	EncryptionKey string
}

func LoadConfig() Config {
	cfg := Config{
		Port:          os.Getenv("PORT"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		JWTSecret:     os.Getenv("JWT_SECRET"),
		EncryptionKey: os.Getenv("ENCRYPTION_KEY"),
	}

	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	mustEnv(cfg.DatabaseURL, "DATABASE_URL")
	mustEnv(cfg.JWTSecret, "JWT_SECRET")
	mustEnv(cfg.EncryptionKey, "ENCRYPTION_KEY")

	return cfg
}

func mustEnv(val, name string) {
	if val == "" {
		fmt.Printf("Missing required environment variable: %s\n", name)
		os.Exit(1)
	}
}
