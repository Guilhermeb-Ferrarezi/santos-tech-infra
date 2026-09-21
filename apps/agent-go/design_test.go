package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestSafeDesignPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "telas"), 0o755); err != nil {
		t.Fatal(err)
	}
	alvo := filepath.Join(dir, "telas", "index.html")
	if err := os.WriteFile(alvo, []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := safeDesignPath(dir, "telas/index.html")
	if err != nil || got != alvo {
		t.Fatalf("caminho válido = %q, %v", got, err)
	}

	for _, ruim := range []string{
		"../../etc/passwd",
		"telas/../../etc/passwd",
		"/etc/passwd",
		"telas/index.html/../../../etc/passwd",
		".git/config",       // fora da whitelist de extensão
		"CLAUDE.md",         // idem: o guia não é servido
		"telas/index.html.", // extensão vazia
	} {
		if _, err := safeDesignPath(dir, ruim); err == nil {
			t.Fatalf("deveria recusar %q", ruim)
		}
	}

	// Symlink apontando para fora do workdir não pode ser servido.
	link := filepath.Join(dir, "telas", "fuga.html")
	if err := os.Symlink("/etc/hostname", link); err == nil {
		if _, err := safeDesignPath(dir, "telas/fuga.html"); err == nil {
			t.Fatal("symlink para fora do workdir deveria falhar")
		}
	}
}

func TestCommitDesignTurn(t *testing.T) {
	dir := t.TempDir()
	s := &Server{cfg: Config{}}
	conv := &Conversation{ID: "c1", Kind: designKind, Workdir: dir}
	if err := s.bootstrapDesignWorkspace(conv); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// Nada mudou desde o bootstrap: não commita e não inventa sha.
	sha, err := s.commitDesignTurn(conv, "primeiro pedido")
	if err != nil {
		t.Fatalf("turno sem mudança devolveu erro: %v", err)
	}
	if sha != "" {
		t.Fatalf("turno sem mudança não deveria commitar, veio %q", sha)
	}

	// Com mudança, commita e devolve o sha.
	tela := filepath.Join(dir, designScreenRel())
	if err := os.WriteFile(tela, []byte("<!doctype html><title>mudou</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err = s.commitDesignTurn(conv, "deixa o título maior")
	if err != nil || sha == "" {
		t.Fatalf("commit falhou: %q %v", sha, err)
	}

	msg, err := gitRun(dir, "log", "-1", "--pretty=%s")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "deixa o título maior") {
		t.Fatalf("mensagem do commit não cita o pedido: %q", msg)
	}
}

func TestCommitDesignTurnEncurtaPrompt(t *testing.T) {
	dir := t.TempDir()
	s := &Server{cfg: Config{}}
	conv := &Conversation{ID: "c1", Kind: designKind, Workdir: dir}
	if err := s.bootstrapDesignWorkspace(conv); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, designScreenRel()), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	longo := strings.Repeat("a", 500) + "\nsegunda linha"
	if _, err := s.commitDesignTurn(conv, longo); err != nil {
		t.Fatalf("commit com prompt longo falhou: %v", err)
	}
	msg, _ := gitRun(dir, "log", "-1", "--pretty=%s")
	if len(msg) > 90 || strings.Contains(msg, "\n") {
		t.Fatalf("assunto do commit não foi encurtado: %d chars", len(msg))
	}
}

func TestInjectInspector(t *testing.T) {
	out := string(injectInspector([]byte("<html><body><h1>oi</h1></body></html>")))
	if !strings.Contains(out, "santos-design-inspect") {
		t.Fatal("script do inspetor não foi injetado")
	}
	if strings.Index(out, "santos-design-inspect") < strings.Index(out, "<h1>oi</h1>") {
		t.Fatal("o script deve entrar depois do conteúdo")
	}
	// Sem </body> o script vai para o fim do documento, não some.
	semBody := string(injectInspector([]byte("<h1>oi</h1>")))
	if !strings.Contains(semBody, "santos-design-inspect") {
		t.Fatal("documento sem </body> perdeu o inspetor")
	}
}

func TestResumoDoPrompt(t *testing.T) {
	// Prompt normal: primeira linha, como sempre foi.
	if got := resumoDoPrompt("deixa o botão azul\ne aumenta a fonte"); got != "deixa o botão azul" {
		t.Fatalf("prompt normal = %q", got)
	}
	if got := resumoDoPrompt("   \n\n"); got != "ajuste no design" {
		t.Fatalf("prompt vazio = %q", got)
	}
	if got := resumoDoPrompt(strings.Repeat("a", 200)); len([]rune(got)) != 72 {
		t.Fatalf("prompt longo não foi truncado: %d runas", len([]rune(got)))
	}

	// Prompt do inspetor: o assunto tem que ser o pedido, não o seletor.
	comAlvo := strings.Join([]string{
		"Elemento selecionado no preview: `main > button.btn`",
		"```html",
		`<button class="btn">Enviar</button>`,
		"```",
		"",
		"deixa o botão azul",
		"e aumenta a fonte",
	}, "\n")
	if got := resumoDoPrompt(comAlvo); got != "deixa o botão azul" {
		t.Fatalf("prompt do inspetor = %q; queria o pedido, não o seletor", got)
	}

	// HTML multi-linha entre as cercas não confunde a extração.
	multi := strings.Join([]string{
		"Elemento selecionado no preview: `section#hero`",
		"```html",
		"<section id=\"hero\">",
		"  <h1>Oi</h1>",
		"</section>",
		"```",
		"",
		"",
		"troca o título",
	}, "\n")
	if got := resumoDoPrompt(multi); got != "troca o título" {
		t.Fatalf("html multi-linha = %q", got)
	}

	// Formatos quebrados caem no comportamento antigo, sem travar.
	for _, quebrado := range []string{
		"Elemento selecionado no preview: `div`\n```html\n<div></div>",             // cerca não fechada
		"Elemento selecionado no preview: `div`\n```html\n<div></div>\n```\n\n   ", // nada depois
	} {
		got := resumoDoPrompt(quebrado)
		if got != "Elemento selecionado no preview: `div`" {
			t.Fatalf("fallback do formato quebrado = %q", got)
		}
	}
}
