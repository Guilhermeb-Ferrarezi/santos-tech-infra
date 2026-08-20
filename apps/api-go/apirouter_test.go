package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/santos-tech/auth/db"
)

func TestClassifyAPIRouterStatus(t *testing.T) {
	unauthorized := []int32{401}
	noCredits := []int32{402, 429}

	cases := []struct {
		code int
		want string
	}{
		{401, apiRouterOutcomeUnauthorized},
		{402, apiRouterOutcomeNoCredits},
		{429, apiRouterOutcomeNoCredits},
		{200, apiRouterOutcomeSuccess},
		{500, apiRouterOutcomeSuccess}, // erro do provider que NÃO é problema de chave — não rotaciona
	}
	for _, c := range cases {
		if got := classifyAPIRouterStatus(unauthorized, noCredits, c.code); got != c.want {
			t.Errorf("classify(%d) = %q, queria %q", c.code, got, c.want)
		}
	}
}

func TestContainsCode(t *testing.T) {
	if !containsCode([]int32{401, 403}, 401) {
		t.Error("deveria conter 401")
	}
	if containsCode([]int32{401, 403}, 500) {
		t.Error("não deveria conter 500")
	}
	if containsCode(nil, 401) {
		t.Error("slice nil nunca contém nada")
	}
}

func TestAuthHeaderValue(t *testing.T) {
	if got := authHeaderValue("Bearer", "sk-123"); got != "Bearer sk-123" {
		t.Errorf("com scheme: %q", got)
	}
	if got := authHeaderValue("", "sk-123"); got != "sk-123" {
		t.Errorf("sem scheme: %q", got)
	}
	if got := authHeaderValue("  ", "sk-123"); got != "sk-123" {
		t.Errorf("scheme só de espaços deveria virar vazio: %q", got)
	}
}

func TestBuildAPIRouterRequest(t *testing.T) {
	provider := db.ApiRouterProvider{
		BaseUrl: "https://api.example.com", AuthHeader: "Authorization", AuthScheme: "Bearer",
	}
	req, err := buildAPIRouterRequest(context.Background(), provider, "sk-abc", http.MethodGet, "/v1/models", nil, "", map[string]string{"X-Extra": "1"})
	if err != nil {
		t.Fatalf("buildAPIRouterRequest: %v", err)
	}
	if req.URL.String() != "https://api.example.com/v1/models" {
		t.Errorf("URL=%s", req.URL.String())
	}
	if req.Header.Get("Authorization") != "Bearer sk-abc" {
		t.Errorf("Authorization=%q", req.Header.Get("Authorization"))
	}
	if req.Header.Get("X-Extra") != "1" {
		t.Errorf("header extra não aplicado")
	}
}

func TestValidInt4(t *testing.T) {
	v := validInt4(401)
	if !v.Valid || v.Int32 != 401 {
		t.Errorf("validInt4(401) = %+v", v)
	}
}

// Smoke via httptest.Server: exercita testAPIRouterKey de ponta a ponta,
// usando um Server mínimo (sem Postgres — s.q fica nil, então precisamos de um
// caminho que não toque o banco em caso de sucesso... na prática
// RecordAPIRouterKeySuccess/Failure TOCAM s.q, então este teste roda contra um
// servidor que sempre responde "outcome" fora dos códigos configurados só pra
// validar a montagem da request e a leitura da resposta; não usamos s.q aqui).
func TestTestAPIRouterKeyMontaRequestCorretamente(t *testing.T) {
	var gotAuth, gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusUnauthorized) // não chega a chamar s.q (unauthorized cai no case, mas s é nil-safe? não — evitamos checar isso aqui)
	}))
	defer srv.Close()

	provider := db.ApiRouterProvider{
		BaseUrl: srv.URL, AuthHeader: "Authorization", AuthScheme: "Bearer",
		UnauthorizedCodes: []int32{401}, NoCreditCodes: []int32{402, 429},
		TestPath: "/v1/models", TestMethod: http.MethodGet,
	}
	secret := "sk-test-999"
	req, err := buildAPIRouterRequest(context.Background(), provider, secret, provider.TestMethod, provider.TestPath, nil, "", nil)
	if err != nil {
		t.Fatalf("buildAPIRouterRequest: %v", err)
	}
	resp, err := apiRouterHTTP.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if gotMethod != http.MethodGet || gotPath != "/v1/models" {
		t.Fatalf("method=%s path=%s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer "+secret {
		t.Fatalf("Authorization=%q", gotAuth)
	}
	if classifyAPIRouterStatus(provider.UnauthorizedCodes, provider.NoCreditCodes, resp.StatusCode) != apiRouterOutcomeUnauthorized {
		t.Fatalf("esperava classify=unauthorized pro status %d", resp.StatusCode)
	}
}

// Sequestro de host: buildAPIRouterRequest manda a chave decifrada no header
// de auth, então um path que troque o host exfiltra a credencial.
func TestAPIRouterRequestURL(t *testing.T) {
	const base = "https://api.provider.com"
	cases := []struct {
		name    string
		base    string
		path    string
		want    string
		wantErr bool
	}{
		{name: "path normal", base: base, path: "/v1/models", want: "https://api.provider.com/v1/models"},
		{name: "com query", base: base, path: "/v1/listen?model=nova-3&smart_format=true", want: "https://api.provider.com/v1/listen?model=nova-3&smart_format=true"},
		{name: "path vazio usa a base", base: base, path: "", want: "https://api.provider.com"},
		{name: "base com prefixo", base: "https://api.provider.com/openai", path: "/v1/models", want: "https://api.provider.com/openai/v1/models"},

		{name: "userinfo sequestra o host", base: base, path: "@evil.com/v1/models", wantErr: true},
		{name: "protocolo-relativa", base: base, path: "//evil.com/x", wantErr: true},
		{name: "sem barra inicial", base: base, path: "v1/models", wantErr: true},
		{name: "url absoluta", base: base, path: "https://evil.com/x", wantErr: true},
		{name: "CRLF no path", base: base, path: "/v1/models\r\nX-Evil: 1", wantErr: true},
		{name: "fragmento", base: base, path: "/v1/models#x", wantErr: true},
		{name: "base_url invalida", base: "nao-e-url", path: "/v1/models", wantErr: true},
	}
	for _, c := range cases {
		got, err := apiRouterRequestURL(c.base, c.path)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: esperava erro, veio %q", c.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: erro inesperado: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q, queria %q", c.name, got, c.want)
		}
	}
}

// O host da URL final nunca pode diferir do host da base_url, aconteça o que
// acontecer com o path.
func TestAPIRouterRequestURLNuncaTrocaHost(t *testing.T) {
	paths := []string{
		"@evil.com/v1/models",
		"//evil.com/x",
		"/../..//evil.com/x",
		"https://evil.com/x",
		"\\evil.com/x",
		"/v1/../../../etc/passwd",
	}
	for _, p := range paths {
		got, err := apiRouterRequestURL("https://api.provider.com", p)
		if err != nil {
			continue // rejeitado: ótimo
		}
		u, perr := url.Parse(got)
		if perr != nil {
			t.Errorf("path %q gerou URL impossível de parsear: %q", p, got)
			continue
		}
		if u.Host != "api.provider.com" || u.User != nil {
			t.Errorf("path %q sequestrou o host: %q", p, got)
		}
	}
}

// buildAPIRouterRequest é o ponto onde a chave decifrada entra no header —
// tem que recusar o path envenenado ANTES de montar a requisição.
func TestBuildAPIRouterRequestRecusaHostSequestrado(t *testing.T) {
	provider := db.ApiRouterProvider{BaseUrl: "https://api.provider.com", AuthHeader: "Authorization", AuthScheme: "Bearer"}
	for _, p := range []string{"@evil.com/v1/models", "//evil.com/x"} {
		req, err := buildAPIRouterRequest(context.Background(), provider, "sk-secreta", http.MethodGet, p, nil, "", nil)
		if err == nil {
			t.Errorf("path %q foi aceito: url=%s auth=%q", p, req.URL, req.Header.Get("Authorization"))
		}
	}
	req, err := buildAPIRouterRequest(context.Background(), provider, "sk-secreta", http.MethodGet, "/v1/models", nil, "", nil)
	if err != nil {
		t.Fatalf("path legítimo recusado: %v", err)
	}
	if req.URL.String() != "https://api.provider.com/v1/models" {
		t.Errorf("url = %s", req.URL)
	}
}

func TestAPIRouterValidPath(t *testing.T) {
	ok := []string{"", "/v1/models", "/v1/listen?model=nova-3", "/v1/text-to-speech/abc?output_format=mp3"}
	bad := []string{"@evil.com/x", "//evil.com/x", "v1/models", "https://evil.com/x", "/x#y", "/x\ny"}
	for _, p := range ok {
		if !apiRouterValidPath(p) {
			t.Errorf("apiRouterValidPath(%q) = false, queria true", p)
		}
	}
	for _, p := range bad {
		if apiRouterValidPath(p) {
			t.Errorf("apiRouterValidPath(%q) = true, queria false", p)
		}
	}
}

// Resposta do provider é lida INTEIRA em memória: sem teto, um provider (ou
// quem sequestrasse a rota) derrubava o processo por OOM.
func TestReadAPIRouterBodyLimite(t *testing.T) {
	b, err := readAPIRouterBody(strings.NewReader("ola"))
	if err != nil || string(b) != "ola" {
		t.Fatalf("corpo pequeno: %q, %v", b, err)
	}

	noLimite := bytes.Repeat([]byte("x"), apiRouterMaxResponseBytes)
	b, err = readAPIRouterBody(bytes.NewReader(noLimite))
	if err != nil || len(b) != apiRouterMaxResponseBytes {
		t.Fatalf("corpo no limite: len=%d, err=%v", len(b), err)
	}

	// Um byte além do teto → erro, nunca truncamento silencioso.
	if _, err := readAPIRouterBody(bytes.NewReader(append(noLimite, 'x'))); !errors.Is(err, errAPIRouterResponseTooLarge) {
		t.Fatalf("corpo acima do teto: err=%v", err)
	}
}

// Rotação: teto total de tempo e de chaves tentadas, para uma requisição não
// ficar presa 30s × N chaves.
func TestAPIRouterRotationBudget(t *testing.T) {
	if apiRouterRotationBudget > 90*time.Second {
		t.Errorf("orçamento da rotação alto demais: %v", apiRouterRotationBudget)
	}
	if apiRouterRotationBudget < apiRouterHTTP.Timeout {
		t.Errorf("orçamento (%v) menor que o timeout de uma única tentativa (%v)", apiRouterRotationBudget, apiRouterHTTP.Timeout)
	}
	if apiRouterMaxKeyAttempts < 2 || apiRouterMaxKeyAttempts > 10 {
		t.Errorf("teto de tentativas fora do razoável: %d", apiRouterMaxKeyAttempts)
	}
}
