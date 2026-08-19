package main

// Testes do roteador de chaves de API — só os caminhos de validação que
// retornam ANTES de tocar no banco (padrão do repo: sem Postgres no CI).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santos-tech/auth/db"
)

func apiRouterTestServer() *Server {
	s := testServer(Config{})
	s.vault = newVault("test-vault-secret", "test-vault-salt")
	return s
}

func TestAPIRouterNotConfiguredSemVault(t *testing.T) {
	s := testServer(Config{}) // vault nil (API_VAULT_SECRET vazia)
	w := httptest.NewRecorder()
	s.handleListAPIRouterProviders(w, httptest.NewRequest("GET", "/auth/admin/api-router/providers", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d (queria 503)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "NOT_CONFIGURED") {
		t.Errorf("body inesperado: %s", w.Body.String())
	}
}

func TestAPIRouterPathID(t *testing.T) {
	cases := []struct {
		raw    string
		wantOK bool
	}{
		{"42", true},
		{"0", false},
		{"-1", false},
		{"abc", false},
		{"", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/x", nil)
		r.SetPathValue("id", c.raw)
		_, err := apiRouterPathID(r, "id")
		if (err == nil) != c.wantOK {
			t.Errorf("apiRouterPathID(%q): err=%v, wantOK=%v", c.raw, err, c.wantOK)
		}
	}
}

func TestAPIRouterProviderBodyDefaults(t *testing.T) {
	b := apiRouterProviderBody{Name: "X", BaseURL: "https://x.com"}.defaults()
	if b.AuthHeader != "Authorization" {
		t.Errorf("AuthHeader default = %q", b.AuthHeader)
	}
	if len(b.UnauthorizedCodes) != 1 || b.UnauthorizedCodes[0] != 401 {
		t.Errorf("UnauthorizedCodes default = %v", b.UnauthorizedCodes)
	}
	if len(b.NoCreditCodes) != 2 || b.NoCreditCodes[0] != 402 || b.NoCreditCodes[1] != 429 {
		t.Errorf("NoCreditCodes default = %v", b.NoCreditCodes)
	}
	if b.TestMethod != http.MethodGet {
		t.Errorf("TestMethod default = %q", b.TestMethod)
	}

	// valores explícitos não devem ser sobrescritos
	b2 := apiRouterProviderBody{
		Name: "X", BaseURL: "https://x.com", AuthHeader: "X-Api-Key",
		UnauthorizedCodes: []int32{403}, TestMethod: "POST",
	}.defaults()
	if b2.AuthHeader != "X-Api-Key" || b2.TestMethod != "POST" {
		t.Errorf("defaults sobrescreveu valor explícito: %+v", b2)
	}
	if len(b2.UnauthorizedCodes) != 1 || b2.UnauthorizedCodes[0] != 403 {
		t.Errorf("UnauthorizedCodes explícito não preservado: %v", b2.UnauthorizedCodes)
	}
}

func TestHandleCreateAPIRouterProviderValidation(t *testing.T) {
	s := apiRouterTestServer()

	// corpo inválido → 400
	w := httptest.NewRecorder()
	s.handleCreateAPIRouterProvider(w, httptest.NewRequest("POST", "/x", strings.NewReader("xxx")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido: code=%d", w.Code)
	}

	// sem name → 400
	w2 := httptest.NewRecorder()
	s.handleCreateAPIRouterProvider(w2, httptest.NewRequest("POST", "/x", strings.NewReader(`{"baseUrl":"https://x.com"}`)))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("sem name: code=%d", w2.Code)
	}

	// sem baseUrl → 400
	w3 := httptest.NewRecorder()
	s.handleCreateAPIRouterProvider(w3, httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"OpenAI"}`)))
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("sem baseUrl: code=%d", w3.Code)
	}
}

func TestHandleUpdateAPIRouterProviderIDInvalido(t *testing.T) {
	s := apiRouterTestServer()
	r := httptest.NewRequest("PATCH", "/x", strings.NewReader(`{"name":"X","baseUrl":"https://x.com"}`))
	r.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	s.handleUpdateAPIRouterProvider(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("id inválido: code=%d", w.Code)
	}
}

func TestHandleCreateAPIRouterKeyValidation(t *testing.T) {
	s := apiRouterTestServer()

	// id inválido → 400 (nem chega a decodificar o corpo)
	r := httptest.NewRequest("POST", "/x", strings.NewReader(`{}`))
	r.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	s.handleCreateAPIRouterKey(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("id inválido: code=%d", w.Code)
	}

	// sem label/secret → 400
	r2 := httptest.NewRequest("POST", "/x", strings.NewReader(`{"label":"","secret":""}`))
	r2.SetPathValue("id", "1")
	w2 := httptest.NewRecorder()
	s.handleCreateAPIRouterKey(w2, r2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("sem label/secret: code=%d", w2.Code)
	}

	// secret grande demais → 400
	big := `{"label":"x","secret":"` + strings.Repeat("a", apiRouterMaxSecretLen+1) + `"}`
	r3 := httptest.NewRequest("POST", "/x", strings.NewReader(big))
	r3.SetPathValue("id", "1")
	w3 := httptest.NewRecorder()
	s.handleCreateAPIRouterKey(w3, r3)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("secret grande: code=%d", w3.Code)
	}
}

func TestHandleSetAPIRouterKeyStatusValidation(t *testing.T) {
	s := apiRouterTestServer()
	r := httptest.NewRequest("POST", "/x", strings.NewReader(`{"status":"unauthorized"}`))
	r.SetPathValue("id", "1")
	r.SetPathValue("keyId", "2")
	w := httptest.NewRecorder()
	s.handleSetAPIRouterKeyStatus(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status fora de active/disabled deveria ser 400: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleUpdateAPIRouterKeyValidation(t *testing.T) {
	s := apiRouterTestServer()
	r := httptest.NewRequest("PATCH", "/x", strings.NewReader(`{"label":""}`))
	r.SetPathValue("id", "1")
	r.SetPathValue("keyId", "2")
	w := httptest.NewRecorder()
	s.handleUpdateAPIRouterKey(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("label vazio: code=%d", w.Code)
	}
}

func TestHandleAPIRouterProxyValidation(t *testing.T) {
	s := apiRouterTestServer()

	// id inválido → 400 (nem chega a decodificar o corpo)
	r := httptest.NewRequest("POST", "/x", strings.NewReader(`{}`))
	r.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	s.handleAPIRouterProxy(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("id inválido: code=%d", w.Code)
	}

	// corpo inválido → 400
	r2 := httptest.NewRequest("POST", "/x", strings.NewReader("xxx"))
	r2.SetPathValue("id", "1")
	w2 := httptest.NewRecorder()
	s.handleAPIRouterProxy(w2, r2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido: code=%d", w2.Code)
	}

	// sem path → 400 (nem chega a buscar o provider)
	r3 := httptest.NewRequest("POST", "/x", strings.NewReader(`{"method":"GET"}`))
	r3.SetPathValue("id", "1")
	w3 := httptest.NewRecorder()
	s.handleAPIRouterProxy(w3, r3)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("sem path: code=%d body=%s", w3.Code, w3.Body.String())
	}
}

func TestHandleAPIRouterChatValidation(t *testing.T) {
	s := apiRouterTestServer()

	// id inválido → 400
	r := httptest.NewRequest("POST", "/x", strings.NewReader(`{"prompt":"oi"}`))
	r.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	s.handleAPIRouterChat(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("id inválido: code=%d", w.Code)
	}

	// corpo inválido → 400
	r2 := httptest.NewRequest("POST", "/x", strings.NewReader("xxx"))
	r2.SetPathValue("id", "1")
	w2 := httptest.NewRecorder()
	s.handleAPIRouterChat(w2, r2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido: code=%d", w2.Code)
	}

	// prompt vazio (só espaço) → 400, nem chega a buscar o provider
	r3 := httptest.NewRequest("POST", "/x", strings.NewReader(`{"prompt":"   "}`))
	r3.SetPathValue("id", "1")
	w3 := httptest.NewRecorder()
	s.handleAPIRouterChat(w3, r3)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("prompt vazio: code=%d body=%s", w3.Code, w3.Body.String())
	}
}

func TestHandleCreateAPIRouterProviderChatAdapterInvalido(t *testing.T) {
	s := apiRouterTestServer()
	r := httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"X","baseUrl":"https://x.com","chatAdapter":"nao-existe"}`))
	w := httptest.NewRecorder()
	s.handleCreateAPIRouterProvider(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("chatAdapter inválido: code=%d body=%s", w.Code, w.Body.String())
	}
}

// A listagem/mutação de chaves NÃO pode devolver o segredo cru: o valor
// completo só sai pelo GET .../keys/{keyId}/reveal (sudo + auditoria).
func TestAPIRouterKeyJSONNaoVazaSegredo(t *testing.T) {
	s := apiRouterTestServer()
	enc, err := s.vault.EncryptKeySecret("sk-super-secreto-123", 3, "e123")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	m := s.apiRouterKeyJSON(&db.ApiRouterKey{ID: 7, ProviderID: 3, Label: "principal", SecretEnc: enc, SecretTail: "e123"})
	if _, ok := m["secret"]; ok {
		t.Fatalf("apiRouterKeyJSON não pode conter a chave \"secret\": %v", m)
	}
	if m["secretTail"] != "e123" {
		t.Errorf("secretTail = %v", m["secretTail"])
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "sk-super-secreto-123") {
		t.Fatalf("segredo cru vazou no JSON: %s", raw)
	}
}

func TestRevealAPIRouterKeySemVault(t *testing.T) {
	s := testServer(Config{}) // vault nil
	w := httptest.NewRecorder()
	s.handleRevealAPIRouterKey(w, httptest.NewRequest("GET", "/auth/admin/api-router/providers/1/keys/2/reveal", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d (queria 503)", w.Code)
	}
}

func TestRevealAPIRouterKeyIDInvalido(t *testing.T) {
	s := apiRouterTestServer()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/auth/admin/api-router/providers/x/keys/y/reveal", nil)
	r.SetPathValue("id", "abc")
	r.SetPathValue("keyId", "abc")
	s.handleRevealAPIRouterKey(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400)", w.Code)
	}
}
