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

// O cache é indexado pelo cookie access_token, então /auth/me tem que validar
// SÓ esse cookie. Se o Authorization da request também fosse repassado, um
// cookie qualquer + Bearer de admin gravaria "ok" para aquele cookie, e o
// cookie sozinho passaria até o TTL — mesmo depois de revogar o Bearer.
func TestSessionAuthCacheNaoHerdaBearer(t *testing.T) {
	// Auth fake que honra o Bearer quando presente (a precedência do auth
	// central é detalhe dele; o bot-go não pode depender disso).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer admin-valido" {
			_, _ = w.Write([]byte(`{"user":{"role":3}}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	a := NewSessionAuth(srv.URL, time.Second, time.Minute)

	combo := reqWithToken("cookie-qualquer")
	combo.Header.Set("Authorization", "Bearer admin-valido")
	if a.Authorized(combo) {
		t.Fatal("cookie inválido + Bearer de admin não pode autorizar a sessão por cookie")
	}
	if a.Authorized(reqWithToken("cookie-qualquer")) {
		t.Fatal("cookie sozinho passou herdando o Bearer da request anterior")
	}
}

// /auth/me recebe exatamente a credencial que vira chave do cache: só o
// cookie access_token — nem Authorization, nem os demais cookies.
func TestSessionAuthRepassaSoOCookieDeSessao(t *testing.T) {
	var gotAuth, gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCookie = r.Header.Get("Cookie")
		_, _ = w.Write([]byte(`{"user":{"role":3}}`))
	}))
	defer srv.Close()

	r := reqWithToken("tok-sessao")
	r.AddCookie(&http.Cookie{Name: "refresh_token", Value: "tok-refresh"})
	r.Header.Set("Authorization", "Bearer outro")
	if !NewSessionAuth(srv.URL, time.Second, time.Minute).Authorized(r) {
		t.Fatal("admin deveria passar")
	}
	if gotAuth != "" {
		t.Fatalf("Authorization repassado ao /auth/me: %q", gotAuth)
	}
	if gotCookie != "access_token=tok-sessao" {
		t.Fatalf("Cookie repassado = %q, quer só access_token=tok-sessao", gotCookie)
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
