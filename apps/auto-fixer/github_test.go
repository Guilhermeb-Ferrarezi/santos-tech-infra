package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestParseRepoURL(t *testing.T) {
	cases := []struct {
		in, owner, repo string
		ok              bool
	}{
		{"https://github.com/Santos-Techrp/bot-go.git", "Santos-Techrp", "bot-go", true},
		{"https://github.com/Guilhermeb-Ferrarezi/rifas", "Guilhermeb-Ferrarezi", "rifas", true},
		{"git@github.com:Santos-Techrp/api-go.git", "Santos-Techrp", "api-go", true},
		{"https://github.com/", "", "", false},
		{"sem-barra", "", "", false},
	}
	for _, c := range cases {
		o, r, err := parseRepoURL(c.in)
		if c.ok {
			if err != nil || o != c.owner || r != c.repo {
				t.Errorf("%q: got (%q,%q,%v)", c.in, o, r, err)
			}
		} else if err == nil {
			t.Errorf("%q: esperava erro", c.in)
		}
	}
}

func TestRepoCloneURL(t *testing.T) {
	cases := map[string]string{
		"Santos-Techrp/dashboard":                "https://github.com/Santos-Techrp/dashboard.git",
		"Guilhermeb-Ferrarezi/santos-tech-infra": "https://github.com/Guilhermeb-Ferrarezi/santos-tech-infra.git",
		"owner/repo.git":                         "https://github.com/owner/repo.git",
		"https://github.com/owner/repo.git":      "https://github.com/owner/repo.git",
		"git@github.com:owner/repo.git":          "git@github.com:owner/repo.git",
	}
	for in, want := range cases {
		if got := repoCloneURL(in); got != want {
			t.Errorf("repoCloneURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseRepoURLAcceptsOwnerRepo(t *testing.T) {
	// A Coolify entrega "owner/repo"; parseRepoURL precisa extrair os dois.
	o, r, err := parseRepoURL("Santos-Techrp/dashboard")
	if err != nil || o != "Santos-Techrp" || r != "dashboard" {
		t.Fatalf("got (%q,%q,%v)", o, r, err)
	}
}

func TestGitHubAppDisabledWhenUnset(t *testing.T) {
	g, err := NewGitHubApp("", "")
	if err != nil || g.Enabled() {
		t.Fatalf("App sem config deveria ser disabled: g=%v err=%v", g, err)
	}
}

func genTestKeyPEM(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(k)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

func TestGitHubAppJWT(t *testing.T) {
	g, err := NewGitHubApp("12345", genTestKeyPEM(t))
	if err != nil || !g.Enabled() {
		t.Fatalf("App deveria habilitar: err=%v", err)
	}
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	tok, err := g.appJWT(now)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.Parse(tok, func(token *jwt.Token) (any, error) {
		return &g.key.PublicKey, nil
	}, jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil || !parsed.Valid {
		t.Fatalf("JWT inválido: %v", err)
	}
	iss, _ := parsed.Claims.GetIssuer()
	if iss != "12345" {
		t.Errorf("issuer=%q", iss)
	}
}

func TestRepoAllowed(t *testing.T) {
	cfg := Config{AllowedRepos: []string{"santos-tech/infra", "santos-tech/bot-go"}}
	ok := []string{
		"santos-tech/infra",
		"Santos-Tech/Infra",
		"https://github.com/santos-tech/bot-go.git",
		"git@github.com:santos-tech/bot-go.git",
	}
	for _, s := range ok {
		if !cfg.repoAllowed(s) {
			t.Errorf("repoAllowed(%q) deveria ser true", s)
		}
	}
	bad := []string{"", "atacante/exfil", "https://github.com/atacante/exfil.git", "santos-tech/infra-evil"}
	for _, s := range bad {
		if cfg.repoAllowed(s) {
			t.Errorf("repoAllowed(%q) deveria ser false", s)
		}
	}
	if (Config{}).repoAllowed("santos-tech/infra") {
		t.Error("allow-list vazia deve negar tudo (fail-closed)")
	}
}
