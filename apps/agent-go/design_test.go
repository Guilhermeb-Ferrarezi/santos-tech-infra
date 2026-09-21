package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
