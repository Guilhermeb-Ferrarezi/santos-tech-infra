package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

func TestDotfyParseWebhook_SemSecret(t *testing.T) {
	// Sem secret (dev), não verifica assinatura — apenas parseia.
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

func TestDotfyParseWebhook_AssinaturaHMAC(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"event":"CHARGE_PAID","data":{"id":"ch_1","correlationID":"stpay_abc"}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	p := &dotfyProvider{secret: secret, sigHeader: "X-Signature"}

	// assinatura válida → passa (aceita também o prefixo "sha256=")
	if _, err := p.ParseWebhook(map[string][]string{"X-Signature": {"sha256=" + sig}}, body); err != nil {
		t.Fatalf("assinatura válida deveria passar: %v", err)
	}
	// ausente → rejeita
	if _, err := p.ParseWebhook(map[string][]string{}, body); err == nil {
		t.Fatal("assinatura ausente deveria falhar")
	}
	// inválida → rejeita
	if _, err := p.ParseWebhook(map[string][]string{"X-Signature": {"deadbeef"}}, body); err == nil {
		t.Fatal("assinatura inválida deveria falhar")
	}
	// corpo adulterado com assinatura do corpo original → rejeita
	if _, err := p.ParseWebhook(map[string][]string{"X-Signature": {sig}}, []byte(`{"event":"CHARGE_PAID","data":{"id":"ch_1","correlationID":"OUTRO"}}`)); err == nil {
		t.Fatal("corpo adulterado deveria falhar")
	}
}
