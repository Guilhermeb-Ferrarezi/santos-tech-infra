package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBootstrapDesignWorkspace(t *testing.T) {
	dir := t.TempDir()
	s := &Server{cfg: Config{}}
	conv := &Conversation{ID: "c1", Kind: designKind, Workdir: dir}

	if err := s.bootstrapDesignWorkspace(conv); err != nil {
		t.Fatalf("bootstrap falhou: %v", err)
	}

	for _, rel := range []string{"CLAUDE.md", "design.json", "telas/index.html", ".git"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("faltou %s: %v", rel, err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "design.json"))
	if err != nil {
		t.Fatalf("ler design.json: %v", err)
	}
	var man map[string]any
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatalf("design.json inválido: %v", err)
	}
	if man["screen"] != "telas/index.html" {
		t.Fatalf("screen errado no manifesto: %v", man["screen"])
	}

	// O bootstrap já deixa um commit inicial — o histórico nunca começa vazio.
	out, err := gitRun(dir, "rev-parse", "HEAD")
	if err != nil || len(out) < 7 {
		t.Fatalf("commit inicial ausente: %q %v", out, err)
	}
}

func TestBootstrapDesignWorkspaceIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := &Server{cfg: Config{}}
	conv := &Conversation{ID: "c1", Kind: designKind, Workdir: dir}
	if err := s.bootstrapDesignWorkspace(conv); err != nil {
		t.Fatalf("1º bootstrap: %v", err)
	}
	marca := filepath.Join(dir, "telas", "index.html")
	if err := os.WriteFile(marca, []byte("<!doctype html><title>editado</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.bootstrapDesignWorkspace(conv); err != nil {
		t.Fatalf("2º bootstrap: %v", err)
	}
	raw, _ := os.ReadFile(marca)
	if string(raw) != "<!doctype html><title>editado</title>" {
		t.Fatal("bootstrap sobrescreveu a tela existente")
	}
}

func TestPreviewTokenRoundTrip(t *testing.T) {
	const secret = "segredo-de-teste"
	tok := previewToken(secret, "conv-1", time.Minute)
	if err := verifyPreviewToken(secret, "conv-1", tok); err != nil {
		t.Fatalf("token válido rejeitado: %v", err)
	}
	if err := verifyPreviewToken(secret, "conv-2", tok); err == nil {
		t.Fatal("token de outra conversa deveria falhar")
	}
	if err := verifyPreviewToken("outro-segredo", "conv-1", tok); err == nil {
		t.Fatal("token com outro segredo deveria falhar")
	}
	if err := verifyPreviewToken(secret, "conv-1", tok+"ff"); err == nil {
		t.Fatal("token adulterado deveria falhar")
	}
	if err := verifyPreviewToken(secret, "conv-1", "lixo"); err == nil {
		t.Fatal("token sem formato deveria falhar")
	}
}

func TestPreviewTokenExpira(t *testing.T) {
	const secret = "segredo-de-teste"
	tok := previewToken(secret, "conv-1", -time.Second) // já nasceu vencido
	if err := verifyPreviewToken(secret, "conv-1", tok); err == nil {
		t.Fatal("token expirado deveria falhar")
	}
}
