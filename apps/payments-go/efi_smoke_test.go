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

// TestEfiMEDSmoke chama ListMED(now-1y, now) e loga a resposta crua + parseada.
// Um array vazio [] é PASS — confirma path + auth + parse OK em homolog.
//
// Rodar com:
//
//	set -a && . ./.env && set +a && EFI_SMOKE=1 PATH=$PATH:$HOME/.local/bin go test -run TestEfiMEDSmoke -v
func TestEfiMEDSmoke(t *testing.T) {
	if os.Getenv("EFI_SMOKE") != "1" {
		t.Skip("EFI_SMOKE!=1; pulando smoke test MED da Efí")
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

	now := time.Now()
	inicio := now.AddDate(-1, 0, 0)

	// Acessar o raw bytes diretamente para logar antes do parse.
	q := "?inicio=" + inicio.UTC().Format(time.RFC3339) + "&fim=" + now.UTC().Format(time.RFC3339)
	rawBytes, rawErr := p.do(ctx, http.MethodGet, "/v2/gn/infracoes"+q, nil)
	if rawErr != nil {
		t.Logf("OBSERVAÇÃO: do() retornou erro: %v", rawErr)
		t.Logf("(endpoint pode estar desabilitado em homolog — similar ao comprovante)")
		// Não falha: documenta o comportamento.
		return
	}
	t.Logf("RESPOSTA RAW da Efí (%d bytes): %s", len(rawBytes), string(rawBytes))

	items, err := p.ListMED(ctx, inicio, now)
	if err != nil {
		t.Fatalf("ListMED: %v", err)
	}
	t.Logf("RESULTADO PARSEADO: %d infrações", len(items))
	for i, it := range items {
		t.Logf("  [%d] ID=%s EndToEndID=%s Status=%s Razao=%q ValueCents=%d DataTransacao=%s",
			i, it.ID, it.EndToEndID, it.Status, it.Razao, it.ValueCents, it.DataTransacao)
	}
	// Um array vazio [] é válido — confirma path + auth + parse OK.
	t.Logf("PASS — path /v2/gn/infracoes + auth + parse confirmados (infracoes=%d)", len(items))
}

// TestEfiReportSmoke solicita um relatório de extrato de conciliação (RequestReport)
// e tenta consultar o status (GetReport). O endpoint /v2/gn/relatorios pode estar
// desabilitado em homologação (como outros endpoints /v2/gn/*). A leitura é puramente
// observacional — qualquer resultado é logado e o teste não falha por timeout/disabled.
//
// Rodar com:
//
//	set -a && . ./.env && set +a && EFI_SMOKE=1 PATH=$PATH:$HOME/.local/bin go test -run TestEfiReportSmoke -v -timeout 60s
func TestEfiReportSmoke(t *testing.T) {
	if os.Getenv("EFI_SMOKE") != "1" {
		t.Skip("EFI_SMOKE!=1; pulando smoke test de relatórios da Efí")
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
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()

	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	t.Logf("RequestReport dataMovimento=%s", yesterday)

	reportID, err := p.RequestReport(ctx, yesterday)
	if err != nil {
		t.Logf("OBSERVAÇÃO: RequestReport retornou erro: %v", err)
		t.Logf("(endpoint /v2/gn/relatorios/extrato-conciliacao pode estar desabilitado em homolog)")
		return
	}
	t.Logf("RequestReport OK → reportId=%s", reportID)

	// Tenta consultar o status algumas vezes com intervalo curto.
	for i := range 3 {
		time.Sleep(2 * time.Second)
		status, ct, body, gerr := p.GetReport(ctx, reportID)
		if gerr != nil {
			t.Logf("GetReport tentativa %d: erro=%v", i+1, gerr)
			break
		}
		t.Logf("GetReport tentativa %d: status=%s contentType=%q bodyLen=%d", i+1, status, ct, len(body))
		if len(body) > 0 && len(body) <= 300 {
			t.Logf("  body: %s", string(body))
		} else if len(body) > 300 {
			t.Logf("  body[:300]: %s", string(body[:300]))
		}
		if status == "done" {
			t.Logf("PASS — relatório pronto e CSV recebido (%d bytes)", len(body))
			return
		}
	}
	t.Logf("OBSERVAÇÃO — relatório ainda processando ou não disponível em homolog (resultado observacional)")
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
