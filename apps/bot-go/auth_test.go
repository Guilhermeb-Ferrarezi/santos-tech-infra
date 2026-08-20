package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func reqWithToken(tok string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
	if tok != "" {
		r.AddCookie(&http.Cookie{Name: "access_token", Value: tok})
	}
	return r
}

func TestSessionAuthFailClosed(t *testing.T) {
	// URL vazia → nega.
	if NewSessionAuth("", time.Second, time.Minute).Authorized(reqWithToken("x")) {
		t.Fatal("URL vazia deveria negar")
	}
	// Sem cookie → nega sem sequer chamar o auth.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("não deveria chamar /auth/me sem cookie")
	}))
	defer srv.Close()
	if NewSessionAuth(srv.URL, time.Second, time.Minute).Authorized(reqWithToken("")) {
		t.Fatal("sem cookie deveria negar")
	}
}

func TestSessionAuthRoles(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"admin", 200, `{"user":{"role":3}}`, true},
		{"usuario comum", 200, `{"user":{"role":1}}`, false},
		{"sessao invalida", 401, `{}`, false},
		{"json quebrado", 200, `nao-e-json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			a := NewSessionAuth(srv.URL, time.Second, time.Minute)
			if got := a.Authorized(reqWithToken("tok-" + tc.name)); got != tc.want {
				t.Fatalf("Authorized = %v, quer %v", got, tc.want)
			}
		})
	}
}

func TestSessionAuthCache(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"user":{"role":3}}`))
	}))
	defer srv.Close()

	a := NewSessionAuth(srv.URL, time.Second, time.Minute)
	for i := 0; i < 3; i++ {
		if !a.Authorized(reqWithToken("mesmo-token")) {
			t.Fatal("admin deveria passar")
		}
	}
	if hits != 1 {
		t.Fatalf("esperava 1 chamada ao /auth/me (cache), teve %d", hits)
	}
	// Token diferente não reaproveita a entrada do cache.
	a.Authorized(reqWithToken("outro-token"))
	if hits != 2 {
		t.Fatalf("token novo deveria revalidar; hits=%d", hits)
	}
}
