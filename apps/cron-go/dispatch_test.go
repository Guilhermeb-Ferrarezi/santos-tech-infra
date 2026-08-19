package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

	// Sufixo escrito SEM o ponto inicial (a forma natural de configurar a env)
	// tem que se comportar igual: casar o domínio e seus subdomínios, e nada
	// que só termine com a mesma string. Antes, "santos-tech.com" deixava
	// "evilsantos-tech.com" passar — e todo dispatch leva o CRON_SERVICE_PAT.
	semPonto := []string{"santos-tech.com"}
	semPontoCases := map[string]bool{
		"santos-tech.com":          true,
		"payments.santos-tech.com": true,
		"evilsantos-tech.com":      false,
		"xsantos-tech.com":         false,
		"santos-tech.com.evil.com": false,
	}
	for host, want := range semPontoCases {
		if got := hostAllowed(host, semPonto); got != want {
			t.Errorf("hostAllowed(%q, %v)=%v, queria %v", host, semPonto, got, want)
		}
	}
	// A forma com ponto continua igualmente estrita.
	if hostAllowed("evilsantos-tech.com", allow) {
		t.Error("allowlist com ponto não deveria casar evilsantos-tech.com")
	}
	if !hostAllowed("santos-tech.com", allow) {
		t.Error("allowlist \".santos-tech.com\" deveria cobrir o domínio raiz")
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

// TestDispatchRedirectSSRFBlocked verifica que um redirect 302 para um host que
// o hostCheck reprova é bloqueado antes de ser seguido.
// Configuração:
//   - ts: servidor que responde 302 Location: http://169.254.169.254/
//   - hostCheck: permite o host do test server (127.0.0.1:PORT) mas bloqueia
//     qualquer destino que contenha "169.254" — simula o guard anti-SSRF real
//     sem depender de resolução DNS real para o IP de link-local.
func TestDispatchRedirectSSRFBlocked(t *testing.T) {
	// Servidor de teste que emite o redirect malicioso.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer ts.Close()

	tsHost := hostOf(ts.URL)

	Catalog["redirect.action"] = CatalogAction{
		ID:     "redirect.action",
		Label:  "redirect test",
		Method: "GET",
		Scheme: "http",
		Host:   tsHost,
		Path:   "/",
	}
	defer delete(Catalog, "redirect.action")

	s := &Server{cfg: Config{}}
	// hostCheck: permite o host inicial (test server) mas bloqueia qualquer
	// destino que contenha "169.254" (range link-local / metadata cloud).
	s.hostCheck = func(h string) bool {
		if strings.Contains(h, "169.254") {
			return false
		}
		// permite o servidor de teste (127.0.0.1:PORT)
		return h == tsHost
	}

	job := db.CronJob{ActionKind: "catalog", ActionRef: "redirect.action", TimeoutSecs: 5}
	res := s.dispatch(context.Background(), job)

	if res.Err == nil {
		t.Fatalf("esperava erro de redirect bloqueado, mas dispatch teve sucesso (status=%d)", res.HTTPStatus)
	}
	if !strings.Contains(res.Err.Error(), "redirect bloqueado") {
		t.Fatalf("esperava erro contendo \"redirect bloqueado\" (CheckRedirect), mas got: %v", res.Err)
	}
	t.Logf("redirect bloqueado corretamente: %v", res.Err)
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

// TestDispatchBodyAndHeaders verifica que HttpBody, Params e HttpHeaders são
// realmente enviados ao alvo — e que Authorization Bearer prevalece sobre headers custom.
func TestDispatchBodyAndHeaders(t *testing.T) {
	var gotBody []byte
	var gotCustomHeader string
	var gotAuthHeader string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotCustomHeader = r.Header.Get("X-Custom-Header")
		gotAuthHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	Catalog["body.action"] = CatalogAction{
		ID: "body.action", Label: "body test", Method: "POST",
		Scheme: "http", Host: hostOf(ts.URL), Path: "/",
	}
	defer delete(Catalog, "body.action")

	t.Run("HttpBody enviado", func(t *testing.T) {
		gotBody = nil
		gotCustomHeader = ""
		gotAuthHeader = ""

		s := &Server{cfg: Config{ServicePAT: "test-pat"}}
		s.hostCheck = func(string) bool { return true }
		job := db.CronJob{
			ActionKind:  "catalog",
			ActionRef:   "body.action",
			TimeoutSecs: 5,
			HttpBody:    `{"hello":"world"}`,
			HttpHeaders: []byte(`{"X-Custom-Header":"valor-custom"}`),
		}
		res := s.dispatch(context.Background(), job)
		if res.Err != nil {
			t.Fatalf("dispatch falhou: %v", res.Err)
		}
		if string(gotBody) != `{"hello":"world"}` {
			t.Errorf("body recebido=%q, queria %q", gotBody, `{"hello":"world"}`)
		}
		if gotCustomHeader != "valor-custom" {
			t.Errorf("X-Custom-Header=%q, queria %q", gotCustomHeader, "valor-custom")
		}
		// Bearer deve prevalecer — nunca sobrescrito por header custom.
		if gotAuthHeader != "Bearer test-pat" {
			t.Errorf("Authorization=%q, queria %q", gotAuthHeader, "Bearer test-pat")
		}
	})

	t.Run("Params enviado quando HttpBody vazio", func(t *testing.T) {
		gotBody = nil
		gotCustomHeader = ""

		s := &Server{cfg: Config{ServicePAT: "test-pat"}}
		s.hostCheck = func(string) bool { return true }
		job := db.CronJob{
			ActionKind:  "catalog",
			ActionRef:   "body.action",
			TimeoutSecs: 5,
			HttpBody:    "",
			Params:      []byte(`{"param":"valor"}`),
		}
		res := s.dispatch(context.Background(), job)
		if res.Err != nil {
			t.Fatalf("dispatch falhou: %v", res.Err)
		}
		if string(gotBody) != `{"param":"valor"}` {
			t.Errorf("body (params) recebido=%q, queria %q", gotBody, `{"param":"valor"}`)
		}
	})

	t.Run("Params vazio/nulo não envia corpo", func(t *testing.T) {
		gotBody = nil

		s := &Server{cfg: Config{ServicePAT: "test-pat"}}
		s.hostCheck = func(string) bool { return true }
		job := db.CronJob{
			ActionKind:  "catalog",
			ActionRef:   "body.action",
			TimeoutSecs: 5,
			HttpBody:    "",
			Params:      []byte(`{}`),
		}
		res := s.dispatch(context.Background(), job)
		if res.Err != nil {
			t.Fatalf("dispatch falhou: %v", res.Err)
		}
		if len(gotBody) != 0 {
			t.Errorf("esperava body vazio, mas recebeu: %q", gotBody)
		}
	})
}
