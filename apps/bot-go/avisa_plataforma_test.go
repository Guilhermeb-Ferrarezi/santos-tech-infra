package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Em 25/09 o aviso do sino se perdeu porque o api-go estava reiniciando num
// deploy (503 por um ou dois segundos). Erro de servidor tenta de novo; erro do
// pedido (4xx) não adianta repetir.
func TestAvisaNaPlataformaTentaDeNovoNoDeploy(t *testing.T) {
	var chamadas atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if chamadas.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"telefone":"5516991002036"}`))
	}))
	defer srv.Close()
	w := &Worker{deps: WorkerDeps{
		Config: Config{AgentGoURL: srv.URL, PlatformAPIToken: "st_x"},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	esperaAvisoPlataforma = time.Millisecond
	defer func() { esperaAvisoPlataforma = 2 * time.Second }()

	tel, ok := w.avisaNaPlataforma(t.Context(), retornoPendente{ConvID: "c1", Nome: "Ana"}, "https://santos-tech.com/dashboard", 30, "aviso")
	if !ok || tel != "5516991002036" || chamadas.Load() != 3 {
		t.Fatalf("ok=%v tel=%q chamadas=%d (queria sucesso na 3ª)", ok, tel, chamadas.Load())
	}
}

func TestAvisaNaPlataformaNaoRepete4xx(t *testing.T) {
	var chamadas atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chamadas.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	w := &Worker{deps: WorkerDeps{
		Config: Config{AgentGoURL: srv.URL, PlatformAPIToken: "st_x"},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	esperaAvisoPlataforma = time.Millisecond
	defer func() { esperaAvisoPlataforma = 2 * time.Second }()

	if _, ok := w.avisaNaPlataforma(t.Context(), retornoPendente{ConvID: "c1"}, "https://x", 30, "aviso"); ok || chamadas.Load() != 1 {
		t.Errorf("ok=%v chamadas=%d (404 não deve repetir)", ok, chamadas.Load())
	}
}
