package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDotfyCreateCharge(t *testing.T) {
	var gotValue float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(401)
			return
		}
		var req dotfyChargeReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotValue = req.Value
		w.Header().Set("Content-Type", "application/json")
		// envelope real do Dotfy: {success, data{...}}, qrCode = copia-e-cola
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"ch_123","chargeId":"dotfyt_x","correlationID":"dotfyt_x","qrCode":"00020126...","qrCodeImage":"data:image/png;base64,xxx","paymentLink":"https://app.dotfy.com.br/p/ch_123","value":6600}}`))
	}))
	defer srv.Close()

	p := &dotfyProvider{base: srv.URL, key: "test-key", client: srv.Client()}
	// R$539,90 = 53990 centavos → API espera 539.90 REAIS
	res, err := p.CreateCharge(context.Background(), ChargeRequest{CorrelationID: "nosso", AmountCents: 53990, PayerName: "Fulano", PayerTaxID: "00000000000"})
	if err != nil {
		t.Fatal(err)
	}
	if gotValue != 539.90 {
		t.Fatalf("value enviado deveria ser 539.90 reais, veio %v", gotValue)
	}
	if res.ProviderChargeID != "ch_123" || res.BRCode != "00020126..." {
		t.Fatalf("response mal parseado: %+v", res)
	}
	if res.CorrelationID != "dotfyt_x" {
		t.Fatalf("deveria adotar o correlationID do Dotfy, veio %q", res.CorrelationID)
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
	// Formato Dotfy (estilo Stripe): t=<timestamp>,v1=HMAC-SHA256(secret, "<t>.<body>") hex.
	ts := "1781649901121"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	sig := "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
	p := &dotfyProvider{secret: secret, sigHeader: "X-Webhook-Signature"}

	// assinatura válida → passa
	if _, err := p.ParseWebhook(map[string][]string{"X-Webhook-Signature": {sig}}, body); err != nil {
		t.Fatalf("assinatura válida deveria passar: %v", err)
	}
	// ausente → rejeita
	if _, err := p.ParseWebhook(map[string][]string{}, body); err == nil {
		t.Fatal("assinatura ausente deveria falhar")
	}
	// formato inesperado → rejeita
	if _, err := p.ParseWebhook(map[string][]string{"X-Webhook-Signature": {"deadbeef"}}, body); err == nil {
		t.Fatal("assinatura em formato inesperado deveria falhar")
	}
	// v1 inválido → rejeita
	if _, err := p.ParseWebhook(map[string][]string{"X-Webhook-Signature": {"t=" + ts + ",v1=deadbeef"}}, body); err == nil {
		t.Fatal("assinatura inválida deveria falhar")
	}
	// corpo adulterado com assinatura do corpo original → rejeita
	if _, err := p.ParseWebhook(map[string][]string{"X-Webhook-Signature": {sig}}, []byte(`{"event":"CHARGE_PAID","data":{"id":"ch_1","correlationID":"OUTRO"}}`)); err == nil {
		t.Fatal("corpo adulterado deveria falhar")
	}
}
