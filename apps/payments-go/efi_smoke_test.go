package main

import (
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestEfiBalanceSmoke consulta o saldo real da conta Efí (homologação) via GET /v2/gn/saldo.
// Valida que o path e o shape da resposta estão corretos.
//
// Rodar com:
//
//	set -a && . ./.env && set +a && EFI_SMOKE=1 go test -run TestEfiBalanceSmoke -v
func TestEfiBalanceSmoke(t *testing.T) {
	if os.Getenv("EFI_SMOKE") != "1" {
		t.Skip("EFI_SMOKE!=1; pulando smoke test de saldo da Efí")
	}
	cert, err := loadClientCert(os.Getenv("EFI_CERT_P12_BASE64"), os.Getenv("EFI_CERT_PASSWORD"))
	if err != nil {
		t.Fatalf("loadClientCert: %v", err)
	}
	p := &efiProvider{
		base:         strings.TrimRight(os.Getenv("EFI_BASE_URL"), "/"),
		clientID:     os.Getenv("EFI_CLIENT_ID"),
		clientSecret: os.Getenv("EFI_CLIENT_SECRET"),
		pixKey:       os.Getenv("EFI_PIX_KEY"),
		client: &http.Client{
			Timeout:   20 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}}},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cents, err := p.GetBalance(ctx)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	t.Logf("saldo Efí (homolog): %d centavos (R$ %.2f)", cents, float64(cents)/100)
}

// TestEfiSmokeHomolog exercita a API Pix REAL da Efí (homologação): mTLS +
// OAuth + criar uma cobrança + consultar. Valida que os nomes de campo
// assumidos no efiProvider batem com a resposta real do gateway.
//
// Pula por padrão (igual ao TestR2UploadIntegration): precisa das credenciais
// no ambiente e de rede. Rodar com:
//
//	set -a && . ./.env && set +a && EFI_SMOKE=1 go test -run TestEfiSmokeHomolog -v
func TestEfiSmokeHomolog(t *testing.T) {
	if os.Getenv("EFI_SMOKE") != "1" {
		t.Skip("EFI_SMOKE!=1; pulando smoke test da Efí homologação")
	}
	cert, err := loadClientCert(os.Getenv("EFI_CERT_P12_BASE64"), os.Getenv("EFI_CERT_PASSWORD"))
	if err != nil {
		t.Fatalf("loadClientCert: %v", err)
	}
	p := &efiProvider{
		base:         strings.TrimRight(os.Getenv("EFI_BASE_URL"), "/"),
		clientID:     os.Getenv("EFI_CLIENT_ID"),
		clientSecret: os.Getenv("EFI_CLIENT_SECRET"),
		pixKey:       os.Getenv("EFI_PIX_KEY"),
		client: &http.Client{
			Timeout:   20 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}}},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	txid := newCorrelationID()
	res, err := p.CreateCharge(ctx, ChargeRequest{
		CorrelationID: txid,
		AmountCents:   100,
		PayerName:     "Teste Smoke",
		Description:   "smoke test homolog",
	})
	if err != nil {
		t.Fatalf("CreateCharge: %v", err)
	}
	t.Logf("txid=%s status=%s brCodeLen=%d hasQR=%v provCharge=%s",
		txid, res.Status, len(res.BRCode), res.QRCode != "", res.ProviderChargeID)
	if res.BRCode == "" {
		t.Error("BRCode (pixCopiaECola) vazio — nome de campo da Efí pode ter mudado")
	}
	if res.Status != "pending" {
		t.Errorf("status esperado pending (ATIVA), veio %q", res.Status)
	}
	if res.ProviderChargeID != txid {
		t.Errorf("ProviderChargeID esperado %q, veio %q", txid, res.ProviderChargeID)
	}

	got, err := p.GetCharge(ctx, txid)
	if err != nil {
		t.Fatalf("GetCharge: %v", err)
	}
	t.Logf("GetCharge: status=%s brCodeLen=%d", got.Status, len(got.BRCode))
	if got.Status != "pending" {
		t.Errorf("GetCharge status esperado pending, veio %q", got.Status)
	}
}

// TestEfiReceiptSmoke observa o que GET /v2/gn/pix/comprovantes?txid=... devolve
// para um txid conhecido (de uma cobrança homolog — normalmente não paga, então
// o objetivo é ver o status HTTP, Content-Type e prefixo do corpo para decidir
// o formato (PDF cru vs JSON com link/base64).
//
// Rodar com:
//
//	set -a && . ./.env && set +a && EFI_SMOKE=1 PATH=$PATH:$HOME/.local/bin go test -run TestEfiReceiptSmoke -v
func TestEfiReceiptSmoke(t *testing.T) {
	if os.Getenv("EFI_SMOKE") != "1" {
		t.Skip("EFI_SMOKE!=1; pulando smoke test de comprovante da Efí")
	}
	cert, err := loadClientCert(os.Getenv("EFI_CERT_P12_BASE64"), os.Getenv("EFI_CERT_PASSWORD"))
	if err != nil {
		t.Fatalf("loadClientCert: %v", err)
	}
	p := &efiProvider{
		base:         strings.TrimRight(os.Getenv("EFI_BASE_URL"), "/"),
		clientID:     os.Getenv("EFI_CLIENT_ID"),
		clientSecret: os.Getenv("EFI_CLIENT_SECRET"),
		pixKey:       os.Getenv("EFI_PIX_KEY"),
		client: &http.Client{
			Timeout:   20 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}}},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Cria uma cobrança para obter um txid real (em homolog, ela não estará paga).
	txid := newCorrelationID()
	_, err = p.CreateCharge(ctx, ChargeRequest{
		CorrelationID: txid,
		AmountCents:   100,
		PayerName:     "Smoke Receipt",
		Description:   "smoke comprovante",
	})
	if err != nil {
		t.Fatalf("CreateCharge: %v", err)
	}
	t.Logf("txid criado: %s", txid)

	// Chama GetReceipt diretamente para observar o que a Efí devolve.
	// Em homolog com cobrança não paga, esperamos um erro (4xx). Logamos tudo.
	contentType, body, receiptErr := p.GetReceipt(ctx, txid)
	if receiptErr != nil {
		t.Logf("OBSERVAÇÃO: GetReceipt retornou erro: %v", receiptErr)
		t.Logf("(esperado para cobrança não paga em homolog — endpoint pode retornar 404/422)")
		// Não falhar — o objetivo é observar, não garantir 200.
		return
	}

	prefix := string(body)
	if len(prefix) > 200 {
		prefix = prefix[:200]
	}
	t.Logf("OBSERVAÇÃO: GetReceipt OK")
	t.Logf("  Content-Type: %s", contentType)
	t.Logf("  Body length: %d bytes", len(body))
	t.Logf("  Body prefix: %q", prefix)
}
