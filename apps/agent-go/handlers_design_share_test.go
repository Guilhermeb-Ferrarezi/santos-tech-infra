package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func escreve(t *testing.T, dir, rel, conteudo string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(conteudo), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListDesignScreensIndexPrimeiroETitulo(t *testing.T) {
	dir := t.TempDir()
	escreve(t, dir, "telas/login.html", "<html><head><title>  Entrar &amp; cadastrar </title></head></html>")
	escreve(t, dir, "telas/index.html", "<html><head><title>Início</title></head></html>")
	escreve(t, dir, "telas/sem-titulo.html", "<html><body>oi</body></html>")
	escreve(t, dir, "telas/notas.txt", "não é tela")
	escreve(t, dir, "telas/.oculta.html", "<title>x</title>")

	got, err := listDesignScreens(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []designScreen{
		{Path: "telas/index.html", Title: "Início"},
		{Path: "telas/login.html", Title: "Entrar & cadastrar"},
		{Path: "telas/sem-titulo.html", Title: "sem-titulo"},
	}
	if len(got) != len(want) {
		t.Fatalf("telas = %+v", got)
	}
	for i := range want {
		if got[i].Path != want[i].Path || got[i].Title != want[i].Title {
			t.Fatalf("tela %d = %+v, queria %+v", i, got[i], want[i])
		}
		if got[i].UpdatedAt == "" {
			t.Fatalf("tela %d sem updatedAt", i)
		}
	}
}

func TestListDesignScreensSemPastaDevolveVazio(t *testing.T) {
	got, err := listDesignScreens(t.TempDir())
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

const tokValido = "abcdefghijklmnopqrstuvwxyzABCDEF" // 32 chars do alfabeto base64url

func serveShared(t *testing.T, workdir, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	s := &Server{}
	s.shareWorkdirFor = func(_ context.Context, tok string) (string, error) {
		if tok != tokValido {
			return "", appErr(http.StatusNotFound, "NOT_FOUND", "Link inválido ou revogado")
		}
		return workdir, nil
	}
	req := httptest.NewRequest(http.MethodGet, "/claude/share/"+token+"/"+path, nil)
	req.SetPathValue("token", token)
	req.SetPathValue("path", path)
	rec := httptest.NewRecorder()
	s.handleDesignShared(rec, req)
	return rec
}

func TestShareServeTelaComSandboxENoindex(t *testing.T) {
	dir := t.TempDir()
	escreve(t, dir, "telas/index.html", "<html><body>público</body></html>")

	rec := serveShared(t, dir, tokValido, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "público") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, precisa := range []string{"sandbox allow-scripts allow-forms", "frame-ancestors 'none'", "connect-src 'none'"} {
		if !strings.Contains(csp, precisa) {
			t.Fatalf("CSP sem %q: %s", precisa, csp)
		}
	}
	if !strings.Contains(rec.Header().Get("X-Robots-Tag"), "noindex") {
		t.Fatal("link público deveria ter X-Robots-Tag noindex")
	}
	if strings.Contains(rec.Body.String(), "santos-design-inspect") {
		t.Fatal("link público nunca injeta o inspetor")
	}
}

func TestShareRecusaForaDeTelasEAssets(t *testing.T) {
	dir := t.TempDir()
	escreve(t, dir, "telas/index.html", "ok")
	escreve(t, dir, "assets/logo.svg", "<svg/>")
	escreve(t, dir, "CLAUDE.md", "segredo do projeto")
	escreve(t, dir, "outra/pagina.html", "fora")

	if rec := serveShared(t, dir, tokValido, "assets/logo.svg"); rec.Code != http.StatusOK {
		t.Fatalf("assets/ deveria servir; status=%d", rec.Code)
	}
	for _, p := range []string{"outra/pagina.html", "telas/../outra/pagina.html", "telas/../CLAUDE.md", "../telas/index.html/../../outra/pagina.html"} {
		if rec := serveShared(t, dir, tokValido, p); rec.Code != http.StatusNotFound {
			t.Fatalf("%q deveria ser 404, veio %d", p, rec.Code)
		}
	}
}

func TestShareTokenInvalidoOuDesconhecido(t *testing.T) {
	dir := t.TempDir()
	escreve(t, dir, "telas/index.html", "ok")
	for _, tok := range []string{"curto", tokValido + "x", strings.Repeat("!", 32), strings.Repeat("z", 32)} {
		if rec := serveShared(t, dir, tok, "telas/index.html"); rec.Code != http.StatusNotFound {
			t.Fatalf("token %q deveria dar 404, veio %d", tok, rec.Code)
		}
	}
}

func TestNewShareTokenFormato(t *testing.T) {
	a, err := newShareToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newShareToken()
	if !shareTokenRe.MatchString(a) || a == b {
		t.Fatalf("tokens %q %q", a, b)
	}
}

func TestPreviewCSPTemSandbox(t *testing.T) {
	csp := designCSP(Config{CORSOrigins: []string{"https://santos-tech.com"}})
	if !strings.Contains(csp, "sandbox allow-scripts allow-forms") || !strings.Contains(csp, "frame-ancestors https://santos-tech.com") {
		t.Fatalf("CSP do preview: %s", csp)
	}
}
