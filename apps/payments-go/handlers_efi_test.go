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
