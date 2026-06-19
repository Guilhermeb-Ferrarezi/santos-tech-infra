package main

import (
	"os"
	"testing"
)

func TestLoadConfigEFIDefaults(t *testing.T) {
	// obrigatórias para o boot não chamar os.Exit
	os.Setenv("DATABASE_URL", "postgres://x")
	os.Setenv("REDIS_URL", "redis://x")
	os.Setenv("JWT_SECRET", "s")
	os.Setenv("EFI_CLIENT_ID", "cid")
	os.Setenv("EFI_CLIENT_SECRET", "csec")
	os.Setenv("EFI_CERT_P12_BASE64", "YWJj")
	os.Setenv("EFI_PIX_KEY", "chave@pix")
	defer func() {
		for _, k := range []string{"EFI_CLIENT_ID", "EFI_CLIENT_SECRET", "EFI_CERT_P12_BASE64", "EFI_PIX_KEY"} {
			os.Unsetenv(k)
		}
	}()
	cfg := LoadConfig()
	if cfg.EFIBaseURL != "https://pix-h.api.efipay.com.br" {
		t.Fatalf("default homolog esperado, veio %q", cfg.EFIBaseURL)
	}
	if cfg.EFIClientID != "cid" || cfg.EFIPixKey != "chave@pix" {
		t.Fatalf("envs EFI não carregadas: %+v", cfg)
	}
}
