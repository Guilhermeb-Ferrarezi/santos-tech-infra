package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/santos-tech/cron-go/db"
)

func hostOf(raw string) string { u, _ := url.Parse(raw); return u.Host }

func TestHostAllowed(t *testing.T) {
	allow := []string{".santos-tech.com"}
	cases := map[string]bool{
		"payments.santos-tech.com": true,
		"localhost":                false,
		"169.254.169.254":          false,
		"evil.com":                 false,
		"santos-tech.com.evil.com": false,
	}
	for host, want := range cases {
		if got := hostAllowed(host, allow); got != want {
			t.Errorf("hostAllowed(%q)=%v, queria %v", host, got, want)
		}
	}

	// IPs privados/loopback/link-local na allowlist devem continuar bloqueados
	// — o guard anti-SSRF é incondicional e não pode ser desabilitado pela allowlist.
	ssrfCases := []struct {
		host      string
		allowlist []string
	}{
		{"127.0.0.1", []string{"127.0.0.1"}},
		{"127.0.0.1:8080", []string{"127.0.0.1:8080"}},
		{"169.254.169.254", []string{"169.254.169.254"}},
		{"10.0.0.1", []string{"10.0.0.1"}},
		{"192.168.1.1", []string{"192.168.1.1"}},
		{"::1", []string{"::1"}},
		{"localhost", []string{"localhost"}},
	}
	for _, tc := range ssrfCases {
		if hostAllowed(tc.host, tc.allowlist) {
			t.Errorf("hostAllowed(%q, allowlist=%v) = true; IP privado/loopback deve ser bloqueado mesmo na allowlist", tc.host, tc.allowlist)
		}
	}
}

func TestDispatchCatalogSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-pat" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	}))
	defer ts.Close()

	// Injeta uma ação de catálogo apontando para o servidor de teste, com allowlist liberada.
	// Scheme "http" porque httptest.NewServer não usa TLS.
	Catalog["test.action"] = CatalogAction{ID: "test.action", Label: "t", Method: "POST", Scheme: "http", Host: hostOf(ts.URL), Path: "/"}
	defer delete(Catalog, "test.action")

	s := &Server{cfg: Config{ServicePAT: "test-pat"}}
	// Seam de teste: bypassa hostAllowed para o servidor httptest (127.0.0.1).
	// O guard anti-SSRF de produção permanece intacto; só este teste usa o bypass.
	s.hostCheck = func(string) bool { return true }
	job := db.CronJob{ActionKind: "catalog", ActionRef: "test.action", TimeoutSecs: 5}
	res := s.dispatch(context.Background(), job)
	if res.Err != nil || res.HTTPStatus != http.StatusOK {
		t.Fatalf("esperava sucesso 200, veio status=%d err=%v", res.HTTPStatus, res.Err)
	}
}
