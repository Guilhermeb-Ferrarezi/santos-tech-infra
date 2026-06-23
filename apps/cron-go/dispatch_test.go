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

	s := &Server{cfg: Config{ServicePAT: "test-pat", HostAllowlist: []string{hostOf(ts.URL)}}}
	job := db.CronJob{ActionKind: "catalog", ActionRef: "test.action", TimeoutSecs: 5}
	res := s.dispatch(context.Background(), job)
	if res.Err != nil || res.HTTPStatus != http.StatusOK {
		t.Fatalf("esperava sucesso 200, veio status=%d err=%v", res.HTTPStatus, res.Err)
	}
}
