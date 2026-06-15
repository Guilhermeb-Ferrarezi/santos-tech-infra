package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDotfyCreateCharge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ch_123","brCode":"00020126...","qrCodeImage":"data:image/png;base64,xxx","status":"ACTIVE"}`))
	}))
	defer srv.Close()

	p := &dotfyProvider{base: srv.URL, key: "test-key", client: srv.Client()}
	res, err := p.CreateCharge(context.Background(), ChargeRequest{CorrelationID: "abc", AmountCents: 53990, PayerName: "Fulano", PayerTaxID: "00000000000"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ProviderChargeID != "ch_123" || res.BRCode == "" {
		t.Fatalf("response mal parseado: %+v", res)
	}
}

func TestDotfyParseWebhook(t *testing.T) {
	p := &dotfyProvider{}
	body := []byte(`{"event":"CHARGE_PAID","data":{"id":"ch_1","correlationID":"stpay_abc"}}`)
	ev, err := p.ParseWebhook(nil, body)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "CHARGE_PAID" || ev.CorrelationID != "stpay_abc" || ev.ID == "" {
		t.Fatalf("webhook mal parseado: %+v", ev)
	}
}
