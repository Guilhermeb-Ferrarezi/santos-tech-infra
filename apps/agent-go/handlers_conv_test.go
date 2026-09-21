package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeKind(t *testing.T) {
	for _, in := range []string{"", "chat"} {
		got, err := normalizeKind(in)
		if err != nil || got != chatKind {
			t.Fatalf("normalizeKind(%q) = %q, %v; queria %q, nil", in, got, err, chatKind)
		}
	}
	got, err := normalizeKind("design")
	if err != nil || got != designKind {
		t.Fatalf("normalizeKind(design) = %q, %v", got, err)
	}
	if _, err := normalizeKind("outro"); err == nil {
		t.Fatal("kind desconhecido deveria falhar")
	}
	// Espaço em volta não deve virar kind inválido.
	if got, err := normalizeKind("  design  "); err != nil || got != designKind {
		t.Fatalf("kind com espaços = %q, %v", got, err)
	}
}

// Design e repo juntos são recusados antes de qualquer efeito colateral: se
// passassem, prepareWorkspace clonaria o repo e escreveria o PAT do GitHub em
// .mcp.json dentro dele, e o commit por turno do design mandaria o token pro
// histórico de um repositório com origin configurado.
func TestCreateConversationRecusaDesignComRepo(t *testing.T) {
	s := &Server{cfg: Config{}}
	req := httptest.NewRequest(http.MethodPost, "/claude/conversations",
		strings.NewReader(`{"kind":"design","repo":"org/x"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleCreateConversation(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, queria 400; corpo: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "repo") {
		t.Fatalf("erro não explica o motivo: %s", rec.Body.String())
	}
}

// Sem repo o mesmo POST passa da validação (e só então esbarra no resto do
// fluxo): o 400 acima é sobre a combinação, não sobre kind=design.
func TestCreateConversationDesignSemRepoPassaDaValidacao(t *testing.T) {
	if _, err := normalizeKind("design"); err != nil {
		t.Fatalf("kind design deveria ser válido: %v", err)
	}
	var body createConvBody
	req := httptest.NewRequest(http.MethodPost, "/claude/conversations",
		strings.NewReader(`{"kind":"design","repo":"   "}`))
	req.Header.Set("Content-Type", "application/json")
	if err := decodeJSON(req, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.TrimSpace(body.Repo) != "" {
		t.Fatal("repo só com espaços não deveria contar como repo")
	}
}
