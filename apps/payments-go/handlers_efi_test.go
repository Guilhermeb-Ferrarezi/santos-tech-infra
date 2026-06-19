package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeEfiOps struct{}

func (fakeEfiOps) GetBalance(context.Context) (int64, error) { return 12345, nil }
func (fakeEfiOps) GetReceipt(_ context.Context, _ string) (string, []byte, error) {
	return "application/pdf", []byte("%PDF-1.4 fake"), nil
}

func TestHandleEfiBalance(t *testing.T) {
	s := &Server{efi: fakeEfiOps{}}
	w := httptest.NewRecorder()
	s.handleEfiBalance(w, httptest.NewRequest("GET", "/efi/balance", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	var out struct {
		AvailableCents int64 `json:"availableCents"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.AvailableCents != 12345 {
		t.Fatalf("availableCents=%d", out.AvailableCents)
	}
}

// TestHandleReceipt_StoreNil: sem store configurado → 404.
func TestHandleReceipt_StoreNil(t *testing.T) {
	s := &Server{efi: fakeEfiOps{}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/charges/99/receipt", nil)
	req.SetPathValue("id", "99")
	s.handleReceipt(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("esperado 404 com store nil, veio %d", w.Code)
	}
	var out struct {
		Code string `json:"code"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Code != "not_found" {
		t.Fatalf("code esperado not_found, veio %q", out.Code)
	}
}
