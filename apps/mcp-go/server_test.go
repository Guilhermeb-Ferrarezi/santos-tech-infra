package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// authRoundTripper injeta o Authorization em toda chamada do cliente MCP,
// como o Claude Code faz com --header.
type authRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (a *authRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", a.token)
	return a.base.RoundTrip(r)
}

// newTestSession sobe o MCP apontando para APIs fake e conecta um cliente real
// via Streamable HTTP. Devolve a sessão pronta para CallTool/ReadResource.
func newTestSession(t *testing.T, cfg Config, openapi []byte, token string) *mcp.ClientSession {
	t.Helper()
	return newTestSessionFor(t, NewServer(cfg, openapi), token)
}

// newTestSessionFor permite ajustar o *Server antes (ex.: trocar o fetch
// anti-SSRF, que bloquearia o 127.0.0.1 do httptest).
func newTestSessionFor(t *testing.T, s *Server, token string) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	transport := &mcp.StreamableClientTransport{
		Endpoint: srv.URL,
		HTTPClient: &http.Client{
			Transport: &authRoundTripper{token: token, base: http.DefaultTransport},
		},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func toolText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("resultado sem conteúdo")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("conteúdo não é texto: %T", res.Content[0])
	}
	return tc.Text
}

func TestRequireAuth(t *testing.T) {
	srv := httptest.NewServer(NewServer(Config{PublicURL: "https://api.example.com/mcp"}, nil).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("esperava 401 sem Authorization, veio %d", resp.StatusCode)
	}
	// O 401 deve apontar o resource_metadata (RFC 9728) para clientes OAuth.
	want := `resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/mcp"`
	if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, want) {
		t.Fatalf("WWW-Authenticate sem resource_metadata: %q", got)
	}
}

func TestProtectedResourceMetadataEndpoint(t *testing.T) {
	srv := httptest.NewServer(NewServer(Config{PublicURL: "https://api.example.com/mcp"}, nil).Handler())
	defer srv.Close()

	for _, path := range []string{"/.well-known/oauth-protected-resource", "/mcp/.well-known/oauth-protected-resource"} {
		resp, err := http.Get(srv.URL + path) // sem Authorization: é público
		if err != nil {
			t.Fatal(err)
		}
		body := make([]byte, 512)
		n, _ := resp.Body.Read(body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: esperava 200, veio %d", path, resp.StatusCode)
		}
		got := string(body[:n])
		if !strings.Contains(got, `"resource":"https://api.example.com/mcp"`) ||
			!strings.Contains(got, `"authorization_servers":["https://api.example.com"]`) {
			t.Fatalf("%s: metadata inesperado: %s", path, got)
		}
	}
}

func TestHealthSemAuth(t *testing.T) {
	srv := httptest.NewServer(NewServer(Config{}, nil).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: esperava 200, veio %d", resp.StatusCode)
	}
}

func TestListUsersRepassaTokenEScope(t *testing.T) {
	var gotAuth, gotURL string
	fakeAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"users":[{"id":"u1","email":"a@b.com"}]}`))
	}))
	defer fakeAuth.Close()

	session := newTestSession(t, Config{AuthBaseURL: fakeAuth.URL}, nil, "Bearer st_test123")
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_users",
		Arguments: map[string]any{"scope": "all"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool falhou: %s", toolText(t, res))
	}
	if gotAuth != "Bearer st_test123" {
		t.Fatalf("Authorization não repassado: %q", gotAuth)
	}
	if gotURL != "/auth/admin/users?scope=all" {
		t.Fatalf("URL errada: %q", gotURL)
	}
	if !strings.Contains(toolText(t, res), `"a@b.com"`) {
		t.Fatalf("resposta da API não devolvida: %s", toolText(t, res))
	}
}

func TestErroDownstreamViraIsError(t *testing.T) {
	fakeAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"code":"SUDO_REQUIRED","message":"Confirme sua identidade."}`))
	}))
	defer fakeAuth.Close()

	session := newTestSession(t, Config{AuthBaseURL: fakeAuth.URL}, nil, "Bearer st_x")
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("esperava IsError em HTTP 403")
	}
	text := toolText(t, res)
	if !strings.Contains(text, "SUDO_REQUIRED") || !strings.Contains(text, "sudo") {
		t.Fatalf("erro deveria citar SUDO_REQUIRED e a dica de sudo: %s", text)
	}
}

func TestUpdateUserSoEnviaCamposInformados(t *testing.T) {
	var gotBody map[string]any
	var gotMethod, gotPath string
	fakeAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer fakeAuth.Close()

	session := newTestSession(t, Config{AuthBaseURL: fakeAuth.URL}, nil, "Bearer st_x")
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "update_user",
		Arguments: map[string]any{"id": "u42", "suspended": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool falhou: %s", toolText(t, res))
	}
	if gotMethod != "PATCH" || gotPath != "/auth/admin/users/u42" {
		t.Fatalf("chamada errada: %s %s", gotMethod, gotPath)
	}
	if len(gotBody) != 1 || gotBody["suspended"] != true {
		t.Fatalf("corpo deveria ter só suspended=true: %v", gotBody)
	}
}

func TestMailboxSendExigeCorpo(t *testing.T) {
	session := newTestSession(t, Config{}, nil, "Bearer st_x")
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "mailbox_send",
		Arguments: map[string]any{"to": "x@y.com", "subject": "oi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(toolText(t, res), "html ou text") {
		t.Fatalf("esperava erro pedindo html ou text: %s", toolText(t, res))
	}
}

func TestResourceLLMSProxiaComToken(t *testing.T) {
	var gotAuth string
	fakeAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/llms.txt" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte("# Santos Tech — APIs"))
	}))
	defer fakeAuth.Close()

	session := newTestSession(t, Config{AuthBaseURL: fakeAuth.URL}, nil, "Bearer st_doc")
	res, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uriLLMS})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer st_doc" {
		t.Fatalf("Authorization não repassado no resource: %q", gotAuth)
	}
	if len(res.Contents) != 1 || !strings.Contains(res.Contents[0].Text, "Santos Tech") {
		t.Fatalf("conteúdo inesperado: %+v", res.Contents)
	}
}

func TestResourceOpenAPIEmbutido(t *testing.T) {
	yaml := []byte("openapi: 3.1.0")
	session := newTestSession(t, Config{}, yaml, "Bearer st_x")
	res, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uriOpenAPI})
	if err != nil {
		t.Fatal(err)
	}
	if res.Contents[0].Text != "openapi: 3.1.0" {
		t.Fatalf("conteúdo inesperado: %q", res.Contents[0].Text)
	}
}

func TestOpenAPIAusenteNaoRegistraResource(t *testing.T) {
	session := newTestSession(t, Config{}, nil, "Bearer st_x")
	res, err := session.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Resources {
		if r.URI == uriOpenAPI {
			t.Fatal("resource openapi não deveria existir sem o arquivo")
		}
	}
}

func TestTodasAsToolsRegistradas(t *testing.T) {
	session := newTestSession(t, Config{}, nil, "Bearer st_x")
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"whoami", "list_users", "create_user", "update_user", "resend_invite",
		"ecosystem_status", "mailbox_list", "mailbox_read", "mailbox_send",
		"email_metrics", "email_logs", "agent_generate", "agent_conversations",
		"upload_image",
	}
	got := map[string]bool{}
	for _, tl := range res.Tools {
		got[tl.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("tool %q não registrada", name)
		}
	}
	if len(res.Tools) != len(want) {
		t.Errorf("esperava %d tools, achei %d", len(want), len(res.Tools))
	}
}
