package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOAuthASMetadata(t *testing.T) {
	s := testServer(Config{PublicOrigin: "https://api.example.com"})
	w := httptest.NewRecorder()
	s.handleOAuthASMetadata(w, httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil))

	if w.Code != 200 {
		t.Fatalf("esperava 200, veio %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("metadata deve permitir CORS *")
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"issuer":                 "https://api.example.com",
		"authorization_endpoint": "https://api.example.com/oauth/authorize",
		"token_endpoint":         "https://api.example.com/oauth/token",
		"registration_endpoint":  "https://api.example.com/oauth/register",
	} {
		if m[k] != want {
			t.Errorf("%s = %v, esperava %s", k, m[k], want)
		}
	}
	if ms, _ := m["code_challenge_methods_supported"].([]any); len(ms) != 1 || ms[0] != "S256" {
		t.Errorf("code_challenge_methods_supported = %v", m["code_challenge_methods_supported"])
	}
}

func TestProtectedResourceMetadata(t *testing.T) {
	s := testServer(Config{PublicOrigin: "https://api.example.com"})
	w := httptest.NewRecorder()
	s.handleProtectedResourceMCP(w, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource/mcp", nil))

	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["resource"] != "https://api.example.com/mcp" {
		t.Errorf("resource = %v", m["resource"])
	}
	if as, _ := m["authorization_servers"].([]any); len(as) != 1 || as[0] != "https://api.example.com" {
		t.Errorf("authorization_servers = %v", m["authorization_servers"])
	}
}

// TestTruncateRunesDCR verifica que truncateRunes (usada em handleOAuthRegister
// para limitar client_name) trunca no limite de runes, nunca no meio de um
// caractere multibyte — um byte-slice ingênuo corromperia emojis/CJK.
func TestTruncateRunesDCR(t *testing.T) {
	emoji := strings.Repeat("🔑", dcrMaxNameLen+1) // 101 emojis = 404 bytes
	got := truncateRunes(emoji, dcrMaxNameLen)

	runes := []rune(got)
	if len(runes) != dcrMaxNameLen {
		t.Errorf("got %d runes, esperava %d", len(runes), dcrMaxNameLen)
	}
	if strings.ContainsRune(got, '�') {
		t.Error("resultado contém replacement char — truncamento corrompeu UTF-8")
	}

	// Abaixo do limite: string inalterada.
	short := "Santos Tech"
	if truncateRunes(short, dcrMaxNameLen) != short {
		t.Error("string curta não deveria ser alterada")
	}
	// Exatamente no limite: inalterada.
	exact := strings.Repeat("A", dcrMaxNameLen)
	if truncateRunes(exact, dcrMaxNameLen) != exact {
		t.Error("string com exatamente max runes não deveria ser alterada")
	}
}

func TestDCRValidation(t *testing.T) {
	s := testServer(Config{PublicOrigin: "https://api.example.com"})
	cases := []struct {
		name, body, wantErr string
	}{
		{"corpo inválido", `{`, "invalid_client_metadata"},
		{"sem redirect", `{"client_name":"x"}`, "invalid_redirect_uri"},
		{"redirect demais", `{"redirect_uris":["https://a.com/1","https://a.com/2","https://a.com/3","https://a.com/4","https://a.com/5","https://a.com/6"]}`, "invalid_redirect_uri"},
		{"scheme perigoso", `{"redirect_uris":["javascript:alert(1)"]}`, "invalid_redirect_uri"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/oauth/register", strings.NewReader(c.body))
		r.Header.Set("Content-Type", "application/json")
		s.handleOAuthRegister(w, r)
		if w.Code != 400 {
			t.Errorf("%s: esperava 400, veio %d", c.name, w.Code)
			continue
		}
		var m map[string]string
		json.Unmarshal(w.Body.Bytes(), &m)
		if m["error"] != c.wantErr {
			t.Errorf("%s: error=%q, esperava %q", c.name, m["error"], c.wantErr)
		}
	}
}
