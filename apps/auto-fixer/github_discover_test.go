package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestGitHubAppDiscover lista as installations do App e os repos de cada uma —
// diagnóstico para confirmar onde o App foi instalado. Gated por env.
//
//	GH_APP_ID=<id> GH_APP_KEY_FILE=<pem> go test -run TestGitHubAppDiscover -v .
func TestGitHubAppDiscover(t *testing.T) {
	appID := os.Getenv("GH_APP_ID")
	keyFile := os.Getenv("GH_APP_KEY_FILE")
	if appID == "" || keyFile == "" {
		t.Skip("defina GH_APP_ID e GH_APP_KEY_FILE")
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewGitHubApp(appID, string(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	jwtTok, err := g.appJWT(time.Now())
	if err != nil {
		t.Fatal(err)
	}

	var insts []struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := g.get(ctx, jwtTok, "https://api.github.com/app/installations", &insts); err != nil {
		t.Fatalf("listar installations: %v", err)
	}
	t.Logf("App %s tem %d installation(s):", appID, len(insts))
	for _, in := range insts {
		var tr struct {
			Token string `json:"token"`
		}
		url := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", in.ID)
		if err := g.call(ctx, "POST", jwtTok, url, nil, &tr); err != nil {
			t.Logf("  owner=%s id=%d — erro no token: %v", in.Account.Login, in.ID, err)
			continue
		}
		var repos struct {
			Total int `json:"total_count"`
			Repos []struct {
				FullName string `json:"full_name"`
			} `json:"repositories"`
		}
		if err := g.call(ctx, "GET", tr.Token, "https://api.github.com/installation/repositories?per_page=100", nil, &repos); err != nil {
			t.Logf("  owner=%s id=%d — erro nos repos: %v", in.Account.Login, in.ID, err)
			continue
		}
		t.Logf("  owner=%s id=%d → %d repo(s):", in.Account.Login, in.ID, repos.Total)
		for _, r := range repos.Repos {
			t.Logf("      %s", r.FullName)
		}
	}
}
