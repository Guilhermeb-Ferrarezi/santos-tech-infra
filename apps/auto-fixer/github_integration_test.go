package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestGitHubAppIntegration valida o fluxo real do GitHub App contra a API do
// GitHub (App JWT → descobre installation → installation token escopado). Pula
// quando as envs não estão setadas, então não afeta `go test ./...` normal.
//
// Rodar:
//
//	GH_APP_ID=<id> \
//	GH_APP_KEY_FILE=/caminho/para.pem \
//	GH_TEST_REPO=Guilhermeb-Ferrarezi/<repo-onde-o-app-esta-instalado> \
//	PATH=$PATH:$HOME/.local/bin go test -run TestGitHubAppIntegration -v .
func TestGitHubAppIntegration(t *testing.T) {
	appID := os.Getenv("GH_APP_ID")
	keyFile := os.Getenv("GH_APP_KEY_FILE")
	testRepo := os.Getenv("GH_TEST_REPO") // formato owner/repo
	if appID == "" || keyFile == "" || testRepo == "" {
		t.Skip("defina GH_APP_ID, GH_APP_KEY_FILE e GH_TEST_REPO para rodar a integração")
	}

	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("lendo a chave: %v", err)
	}
	g, err := NewGitHubApp(appID, string(keyPEM))
	if err != nil || !g.Enabled() {
		t.Fatalf("App não habilitou: %v", err)
	}

	owner, repo, err := parseRepoURL("https://github.com/" + testRepo)
	if err != nil {
		t.Fatalf("repo inválido: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tok, err := g.RepoToken(ctx, time.Now(), owner, repo)
	if err != nil {
		t.Fatalf("RepoToken falhou (App instalado em %s?): %v", owner, err)
	}
	if len(tok) < 10 {
		t.Fatalf("token suspeito (len=%d)", len(tok))
	}
	// Nunca logamos o token inteiro — só o prefixo, para confirmar o formato.
	t.Logf("OK: installation token gerado para %s (len=%d, prefixo=%s…)", testRepo, len(tok), tok[:8])
}
