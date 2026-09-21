package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// servePreview monta um Server mínimo com um workdir pronto e devolve a resposta
// de um GET no preview. convLookup é injetado para não precisar de Postgres.
func servePreview(t *testing.T, workdir, path, query string) *httptest.ResponseRecorder {
	t.Helper()
	s := &Server{cfg: Config{JWTSecret: "segredo-de-teste"}}
	s.designWorkdirFor = func(_ *http.Request, id string) (string, error) {
		if id != "conv-1" {
			return "", appErr(http.StatusNotFound, "NOT_FOUND", "Não encontrado")
		}
		return workdir, nil
	}
	req := httptest.NewRequest(http.MethodGet, "/claude/designs/conv-1/preview/"+path+query, nil)
	req.SetPathValue("id", "conv-1")
	req.SetPathValue("path", path)
	rec := httptest.NewRecorder()
	s.handleDesignPreview(rec, req)
	return rec
}

func TestPreviewExigeToken(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "telas"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "telas", "index.html"), []byte("<html><body>oi</body></html>"), 0o644)

	if rec := servePreview(t, dir, "telas/index.html", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("sem token = %d, queria 404", rec.Code)
	}
}

func TestPreviewServeHTMLComCSP(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "telas"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "telas", "index.html"), []byte("<html><body>oi</body></html>"), 0o644)
	tok := previewToken("segredo-de-teste", "conv-1", previewTokenTTL)

	rec := servePreview(t, dir, "telas/index.html", "?t="+tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, precisa := range []string{"default-src 'none'", "connect-src 'none'", "frame-ancestors"} {
		if !strings.Contains(csp, precisa) {
			t.Fatalf("CSP sem %q: %s", precisa, csp)
		}
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("faltou nosniff")
	}
	if strings.Contains(rec.Body.String(), "santos-design-inspect") {
		t.Fatal("sem ?inspect=1 o inspetor não deve ser injetado")
	}
}

func TestPreviewInjetaInspetor(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "telas"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "telas", "index.html"), []byte("<html><body>oi</body></html>"), 0o644)
	tok := previewToken("segredo-de-teste", "conv-1", previewTokenTTL)

	rec := servePreview(t, dir, "telas/index.html", "?inspect=1&t="+tok)
	if !strings.Contains(rec.Body.String(), "santos-design-inspect") {
		t.Fatal("inspetor não foi injetado com ?inspect=1")
	}
}

func TestPreviewRecusaArquivoProibido(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("segredos do guia"), 0o644)
	tok := previewToken("segredo-de-teste", "conv-1", previewTokenTTL)

	if rec := servePreview(t, dir, "CLAUDE.md", "?t="+tok); rec.Code != http.StatusNotFound {
		t.Fatalf("CLAUDE.md = %d, queria 404", rec.Code)
	}
}
