package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// genTestP12Base64 gera um par chave/cert self-signed e devolve um .p12 (senha vazia) em base64.
func genTestP12Base64(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-efi"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	raw, err := pkcs12.Modern.Encode(key, cert, nil, "")
	if err != nil {
		t.Fatalf("encode p12: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestLoadClientCert(t *testing.T) {
	b64 := genTestP12Base64(t)
	cert, err := loadClientCert(b64, "")
	if err != nil {
		t.Fatalf("loadClientCert: %v", err)
	}
	if cert.Leaf == nil || cert.Leaf.Subject.CommonName != "test-efi" {
		t.Fatalf("cert sem leaf esperado: %+v", cert.Leaf)
	}
}

func TestLoadClientCertRejeitaBase64Invalido(t *testing.T) {
	if _, err := loadClientCert("não é base64 @@@", ""); err == nil {
		t.Fatal("esperava erro com base64 inválido")
	}
}

// --- Provider tests (Task 3) ---

// newTestEfi monta um efiProvider apontando para um servidor de teste (sem mTLS).
func newTestEfi(base string) *efiProvider {
	return &efiProvider{
		base:         strings.TrimRight(base, "/"),
		clientID:     "cid",
		clientSecret: "csec",
		pixKey:       "chave@pix",
		client:       &http.Client{},
	}
}

func TestEfiCreateChargeAndTokenCache(t *testing.T) {
	var tokenHits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenHits, 1)
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok123", "expires_in": 3600})
	})
	mux.HandleFunc("/v2/cob/", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok123" {
			t.Errorf("auth header: %q", got)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"txid":          strings.TrimPrefix(r.URL.Path, "/v2/cob/"),
			"status":        "ATIVA",
			"pixCopiaECola": "00020126BR-CODE",
			"loc":           map[string]any{"id": 42},
		})
	})
	mux.HandleFunc("/v2/loc/42/qrcode", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"qrcode": "00020126BR-CODE", "imagemQrcode": "data:image/png;base64,AAAA"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := newTestEfi(srv.URL)
	req := ChargeRequest{CorrelationID: "stpay0123456789abcdef0123456789", AmountCents: 1000, Description: "x", ExpiresAt: ""}
	res, err := p.CreateCharge(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateCharge: %v", err)
	}
	if res.BRCode != "00020126BR-CODE" || res.QRCode != "data:image/png;base64,AAAA" {
		t.Fatalf("resultado inesperado: %+v", res)
	}
	if res.ProviderChargeID != req.CorrelationID {
		t.Fatalf("txid esperado %q, veio %q", req.CorrelationID, res.ProviderChargeID)
	}
	if res.Status != "pending" {
		t.Fatalf("status esperado pending, veio %q", res.Status)
	}
	// Segunda cobrança reusa o token cacheado.
	if _, err := p.CreateCharge(context.Background(), req); err != nil {
		t.Fatalf("CreateCharge 2: %v", err)
	}
	if n := atomic.LoadInt32(&tokenHits); n != 1 {
		t.Fatalf("token deveria ser buscado 1x (cache), veio %d", n)
	}
}

func TestEfiCreateChargeProviderError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 3600})
	})
	mux.HandleFunc("/v2/cob/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"nome": "valor_invalido", "mensagem": "Valor não permitido"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p := newTestEfi(srv.URL)
	_, err := p.CreateCharge(context.Background(), ChargeRequest{CorrelationID: "stpay0123456789abcdef0123456789", AmountCents: 1})
	var pe *ProviderError
	if err == nil || !errors.As(err, &pe) || pe.Message != "Valor não permitido" {
		t.Fatalf("esperava ProviderError com a mensagem da Efí, veio %v", err)
	}
}

// TestEfiSendPix verifica que SendPix monta o PUT /v3/gn/pix/{idEnvio} com o corpo
// correto (valor "12.34", pagador.chave = pixKey do provider, favorecido.chave = destino)
// e parseia a resposta (idEnvio/e2eId/status). idEnvio é sanitizado (hífens removidos, ≤35).
func TestEfiSendPix(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	})
	mux.HandleFunc("/v3/gn/pix/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("método esperado PUT, veio %s", r.Method)
		}
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		idEnvio := strings.TrimPrefix(r.URL.Path, "/v3/gn/pix/")
		json.NewEncoder(w).Encode(map[string]any{
			"idEnvio": idEnvio,
			"e2eId":   "E2EABC123",
			"valor":   "12.34",
			"horario": "2026-06-21T12:00:00Z",
			"status":  "EM_PROCESSAMENTO",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := newTestEfi(srv.URL) // pixKey = "chave@pix"
	idEnvio, e2e, status, err := p.SendPix(context.Background(), "uuid-AB-12-cd", 1234, "destino@pix.com", "Saque Santos Tech")
	if err != nil {
		t.Fatalf("SendPix: %v", err)
	}
	// idEnvio sanitizado (hífens removidos).
	if gotPath != "/v3/gn/pix/uuidAB12cd" {
		t.Fatalf("path esperado /v3/gn/pix/uuidAB12cd, veio %q", gotPath)
	}
	if idEnvio != "uuidAB12cd" {
		t.Fatalf("idEnvio devolvido esperado uuidAB12cd, veio %q", idEnvio)
	}
	if e2e != "E2EABC123" {
		t.Fatalf("e2eId esperado E2EABC123, veio %q", e2e)
	}
	if status != "EM_PROCESSAMENTO" {
		t.Fatalf("status esperado EM_PROCESSAMENTO, veio %q", status)
	}
	// Corpo: valor string 2 casas, pagador.chave = pixKey, favorecido.chave = destino.
	if gotBody["valor"] != "12.34" {
		t.Fatalf("valor esperado \"12.34\", veio %v", gotBody["valor"])
	}
	pagador, _ := gotBody["pagador"].(map[string]any)
	if pagador["chave"] != "chave@pix" {
		t.Fatalf("pagador.chave esperado chave@pix, veio %v", pagador["chave"])
	}
	if pagador["infoPagador"] != "Saque Santos Tech" {
		t.Fatalf("infoPagador esperado, veio %v", pagador["infoPagador"])
	}
	favorecido, _ := gotBody["favorecido"].(map[string]any)
	if favorecido["chave"] != "destino@pix.com" {
		t.Fatalf("favorecido.chave esperado destino@pix.com, veio %v", favorecido["chave"])
	}
}

// TestEfiSendPixErroGateway: erro do gateway vira ProviderError (mensagem amigável).
func TestEfiSendPixErroGateway(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 3600})
	})
	mux.HandleFunc("/v3/gn/pix/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"nome": "chave_invalida", "mensagem": "Chave Pix inválida"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p := newTestEfi(srv.URL)
	_, _, _, err := p.SendPix(context.Background(), "idemkey1", 100, "x", "y")
	var pe *ProviderError
	if err == nil || !errors.As(err, &pe) || pe.Message != "Chave Pix inválida" {
		t.Fatalf("esperava ProviderError com a mensagem da Efí, veio %v", err)
	}
}

// TestEfiParseWebhookEnvio: o webhook pix com gnExtras.idEnvio vira PAYOUT_RESULT
// (não CHARGE_PAID), com PayoutStatus mapeado.
func TestEfiParseWebhookEnvio(t *testing.T) {
	p := newTestEfi("http://x")
	body := []byte(`{"pix":[{"endToEndId":"E9","status":"REALIZADO","gnExtras":{"idEnvio":"IDE9"}}]}`)
	evs, err := p.ParseWebhook(nil, body)
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("esperava 1 evento, veio %d", len(evs))
	}
	ev := evs[0]
	if ev.Type != "PAYOUT_RESULT" {
		t.Fatalf("type esperado PAYOUT_RESULT, veio %q", ev.Type)
	}
	if ev.IDEnvio != "IDE9" || ev.E2EID != "E9" || ev.PayoutStatus != "completed" {
		t.Fatalf("evento de envio inesperado: %+v", ev)
	}
	if ev.CorrelationID != "" {
		t.Fatalf("envio não deve ter CorrelationID (não é cobrança), veio %q", ev.CorrelationID)
	}
}

func TestEfiParseWebhookMultiplosPix(t *testing.T) {
	p := newTestEfi("http://x")
	body := []byte(`{"pix":[{"endToEndId":"E1","txid":"stpayAAA"},{"endToEndId":"E2","txid":"stpayBBB"}]}`)
	evs, err := p.ParseWebhook(nil, body)
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("esperava 2 eventos, veio %d", len(evs))
	}
	if evs[0].Type != "CHARGE_PAID" || evs[0].ID != "E1" || evs[0].CorrelationID != "stpayAAA" {
		t.Fatalf("evento 0 inesperado: %+v", evs[0])
	}
}

func TestEfiParseWebhookPingDeTeste(t *testing.T) {
	p := newTestEfi("http://x")
	evs, err := p.ParseWebhook(nil, []byte(`{"evento":"teste_webhook"}`))
	if err != nil {
		t.Fatalf("ping não deveria dar erro: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("ping deveria render 0 eventos, veio %d", len(evs))
	}
}

// --- PIX Automático (recorrência) ---

// TestEfiRecStatusToApp cobre o mapeamento status Efí → vocabulário do app.
func TestEfiRecStatusToApp(t *testing.T) {
	cases := []struct{ in, want string }{
		{"APROVADA", "active"},
		{"REJEITADA", "rejected"},
		{"EXPIRADA", "expired"},
		{"CANCELADA", "canceled"},
		{"CRIADA", "pending_auth"},
		{"", "pending_auth"},         // estado desconhecido → ainda aguardando autorização
		{"QUALQUER", "pending_auth"}, // default
	}
	for _, tc := range cases {
		if got := efiRecStatusToApp(tc.in); got != tc.want {
			t.Errorf("efiRecStatusToApp(%q) = %q, esperado %q", tc.in, got, tc.want)
		}
	}
}

// TestEfiParseRecWebhook: payload com vários itens → um RecEvent por idRec, status mapeado.
func TestEfiParseRecWebhookMultiplos(t *testing.T) {
	p := newTestEfi("http://x")
	body := []byte(`{"recs":[{"idRec":"rec-1","status":"APROVADA"},{"idRec":"rec-2","status":"CANCELADA"}]}`)
	evs, err := p.ParseRecWebhook(nil, body)
	if err != nil {
		t.Fatalf("ParseRecWebhook: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("esperava 2 eventos, veio %d", len(evs))
	}
	if evs[0].EfiIDRec != "rec-1" || evs[0].Status != "active" {
		t.Fatalf("evento 0 inesperado: %+v", evs[0])
	}
	if evs[1].EfiIDRec != "rec-2" || evs[1].Status != "canceled" {
		t.Fatalf("evento 1 inesperado: %+v", evs[1])
	}
}

// TestEfiParseRecWebhook: itens sem idRec são ignorados; ping/vazio → 0 eventos.
func TestEfiParseRecWebhookIgnoraSemIDRec(t *testing.T) {
	p := newTestEfi("http://x")
	evs, err := p.ParseRecWebhook(nil, []byte(`{"recs":[{"status":"APROVADA"}]}`))
	if err != nil {
		t.Fatalf("ParseRecWebhook: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("item sem idRec deveria ser ignorado, veio %d eventos", len(evs))
	}
	evs, err = p.ParseRecWebhook(nil, []byte(`{"recs":[]}`))
	if err != nil {
		t.Fatalf("payload vazio não deveria dar erro: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("payload vazio deveria render 0 eventos, veio %d", len(evs))
	}
}
