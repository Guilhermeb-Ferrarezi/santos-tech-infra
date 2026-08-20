package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// routesForCSRFTest monta o mux real. Sem Postgres não dá para passar pelo
// requireAdmin, então o que se testa aqui é a camada ANTERIOR a ele: o método
// aceito pelo padrão do mux. Um GET em /clear tem que morrer em 405 sem nunca
// alcançar o handler.
func TestAcoesDestrutivasNaoAceitamGET(t *testing.T) {
	srv := httptest.NewServer((&Server{}).Routes())
	defer srv.Close()

	for _, path := range []string{"/clear", "/start", "/stop", "/reveal"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: esperava 405, veio %d", path, resp.StatusCode)
		}
	}
}

func TestRequireNonSimple(t *testing.T) {
	var reached bool
	h := (&Server{}).requireNonSimple(func(http.ResponseWriter, *http.Request) { reached = true })

	// Formulário cross-site: POST com Content-Type simples, sem header custom.
	reached = false
	req := httptest.NewRequest(http.MethodPost, "/clear", strings.NewReader(""))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	h(rec, req)
	if reached {
		t.Fatal("request simples (CSRF) chegou no handler")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("esperava 403, veio %d", rec.Code)
	}

	// Sinais aceitos: qualquer um deles força preflight / não é forjável por form.
	for name, set := range map[string]func(*http.Request){
		"X-Requested-With": func(r *http.Request) { r.Header.Set("X-Requested-With", "santos-tech") },
		"Authorization":    func(r *http.Request) { r.Header.Set("Authorization", "Bearer x") },
		"json":             func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=utf-8") },
	} {
		reached = false
		req := httptest.NewRequest(http.MethodPost, "/clear", strings.NewReader(""))
		set(req)
		h(httptest.NewRecorder(), req)
		if !reached {
			t.Errorf("%s deveria ser aceito", name)
		}
	}
}
