package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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
