package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// newTestCobr monta um efiCobrancas apontando para um servidor de teste.
func newTestCobr(base string) *efiCobrancas {
	return &efiCobrancas{
		base:         strings.TrimRight(base, "/"),
		clientID:     "cid",
		clientSecret: "csec",
		client:       &http.Client{},
	}
}

func TestCobrCreateBoletoAndTokenCache(t *testing.T) {
	var tokenHits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/authorize", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenHits, 1)
		if _, _, ok := r.BasicAuth(); !ok {
			t.Error("authorize sem Basic Auth")
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok123", "expires_in": 600})
	})
	mux.HandleFunc("/v1/charge/one-step", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok123" {
			t.Errorf("auth header: %q", got)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{
				"barcode":   "00000.00000 00000.000000 00000.000000 0 00000000000000",
				"charge_id": 44960090,
				"status":    "waiting",
				"pdf":       map[string]any{"charge": "https://efi/boleto.pdf"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := newTestCobr(srv.URL)
	res, err := p.CreateBoleto(context.Background(), BoletoRequest{
		AmountCents: 5990, PayerName: "Maria", PayerTaxID: "94271564656", DueDate: "2026-07-15",
	})
	if err != nil {
		t.Fatalf("CreateBoleto: %v", err)
	}
	if res.ChargeID != "44960090" {
		t.Fatalf("charge_id esperado 44960090, veio %q", res.ChargeID)
	}
	if res.Line == "" || res.PDFURL != "https://efi/boleto.pdf" || res.Status != "waiting" {
		t.Fatalf("resultado inesperado: %+v", res)
	}
	// Segunda emissão reusa o token cacheado.
	if _, err := p.CreateBoleto(context.Background(), BoletoRequest{AmountCents: 100, PayerName: "X", PayerTaxID: "1", DueDate: "2026-07-15"}); err != nil {
		t.Fatalf("CreateBoleto 2: %v", err)
	}
	if n := atomic.LoadInt32(&tokenHits); n != 1 {
		t.Fatalf("token deveria ser buscado 1x (cache), veio %d", n)
	}
}

func TestCobrCreateBoletoProviderError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/authorize", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 600})
	})
	mux.HandleFunc("/v1/charge/one-step", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"code": 400, "error_description": "CPF inválido"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p := newTestCobr(srv.URL)
	_, err := p.CreateBoleto(context.Background(), BoletoRequest{AmountCents: 1, PayerName: "X", PayerTaxID: "0", DueDate: "2026-07-15"})
	var pe *ProviderError
	if err == nil || !errors.As(err, &pe) || pe.Message != "CPF inválido" {
		t.Fatalf("esperava ProviderError com a mensagem da Efí, veio %v", err)
	}
}

func TestCobrGetNotification(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/authorize", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 600})
	})
	mux.HandleFunc("/v1/notification/abc", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": []map[string]any{
				{"identifiers": map[string]any{"charge_id": 1}, "status": map[string]any{"current": "waiting"}},
				{"identifiers": map[string]any{"charge_id": 1}, "status": map[string]any{"current": "paid"}},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p := newTestCobr(srv.URL)
	evs, err := p.GetNotification(context.Background(), "abc")
	if err != nil {
		t.Fatalf("GetNotification: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("esperava 2 eventos, veio %d", len(evs))
	}
	if evs[1].ChargeID != "1" || evs[1].Status != "paid" {
		t.Fatalf("evento paid inesperado: %+v", evs[1])
	}
}

func TestCobrCancelBoleto(t *testing.T) {
	var canceled atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/authorize", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 600})
	})
	mux.HandleFunc("/v1/charge/77/cancel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("método esperado PUT, veio %s", r.Method)
		}
		canceled.Store(true)
		json.NewEncoder(w).Encode(map[string]any{"code": 200})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p := newTestCobr(srv.URL)
	if err := p.CancelBoleto(context.Background(), "77"); err != nil {
		t.Fatalf("CancelBoleto: %v", err)
	}
	if !canceled.Load() {
		t.Fatal("o endpoint de cancelamento não foi chamado")
	}
}
