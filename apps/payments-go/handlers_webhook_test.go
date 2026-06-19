package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// efiStub satisfaz PaymentProvider só para o handler (store nil → caminho de teste).
type efiStub struct{}

func (efiStub) CreateCharge(_ context.Context, _ ChargeRequest) (ChargeResult, error) {
	return ChargeResult{}, nil
}
func (efiStub) GetCharge(_ context.Context, _ string) (ChargeResult, error) {
	return ChargeResult{}, nil
}
func (efiStub) ParseWebhook(_ map[string][]string, body []byte) ([]WebhookEvent, error) {
	if strings.Contains(string(body), "stpay") {
		return []WebhookEvent{{ID: "E1", Type: "CHARGE_PAID", CorrelationID: "stpayAAA"}}, nil
	}
	return nil, nil
}

func TestHandleWebhookSegredoErrado(t *testing.T) {
	s := &Server{cfg: Config{Production: true, EFIWebhookSecret: "right"}, provider: efiStub{}}
	r := httptest.NewRequest("POST", "/webhooks/efi/pix?hmac=wrong", strings.NewReader(`{"pix":[]}`))
	w := httptest.NewRecorder()
	s.handleWebhook(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("segredo errado deveria dar 401, veio %d", w.Code)
	}
}

func TestHandleWebhookSegredoCerto(t *testing.T) {
	s := &Server{cfg: Config{EFIWebhookSecret: "right"}, provider: efiStub{}} // store nil → 200 cedo
	r := httptest.NewRequest("POST", "/webhooks/efi/pix?hmac=right", strings.NewReader(`{"pix":[{"txid":"stpayAAA"}]}`))
	w := httptest.NewRecorder()
	s.handleWebhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("segredo certo deveria dar 200, veio %d", w.Code)
	}
}
