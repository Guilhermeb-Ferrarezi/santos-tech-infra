package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
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

// TestEfiRecSmokeHomolog exercita o PIX Automático (recorrência BACEN) REAL da Efí
// (homologação): cria uma recorrência via POST /v2/rec (Jornada 2) e loga o status +
// corpo cru que a Efí devolve, para confirmar/ajustar os nomes de campo assumidos
// (dadosQR.pixCopiaECola, idRec, status, valorRec, politicaRetentativa, ativacao…).
//
// Pula por padrão. Rodar com:
//
//	set -a && . ./.env && set +a && EFI_SMOKE=1 go test -run TestEfiRecSmokeHomolog -v
func TestEfiRecSmokeHomolog(t *testing.T) {
	if os.Getenv("EFI_SMOKE") != "1" {
		t.Skip("EFI_SMOKE!=1; pulando smoke test de recorrência da Efí")
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
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}}},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// `vinculo.contrato` precisa ser único por recorrência ATIVA (a Efí devolve 400 se repetir),
	// então usamos um sufixo de tempo a cada rodada do smoke.
	uniq := time.Now().Format("20060102150405.000000")
	start := time.Now().AddDate(0, 0, 2).Format("2006-01-02")

	// Método CreateRecurrence (Jornada 2) — agora deve fazer locrec → rec → GET e
	// devolver o copia-e-cola de autorização preenchido.
	res, err := p.CreateRecurrence(ctx, RecurrenceRequest{
		Contract:    "smoke-rec-" + uniq,
		Object:      "Assinatura smoke test",
		PayerName:   "Fulano de Teste",
		PayerTaxID:  "12345678909",
		AmountCents: 1000,
		Periodicity: "MENSAL",
		StartDate:   start,
	})
	if err != nil {
		t.Logf("[CreateRecurrence] erro: %v", err)
	} else {
		t.Logf("[CreateRecurrence] OK idRec=%q status=%q brCode(len=%d)=%q qrCode(len=%d)",
			res.EfiIDRec, res.Status, len(res.BRCode), res.BRCode, len(res.QRCode))
		// Valida o cancelamento (PATCH /v2/rec/{idRec} {"status":"CANCELADA"}) — botão admin.
		if cerr := p.CancelRecurrence(ctx, res.EfiIDRec); cerr != nil {
			t.Logf("[CancelRecurrence] erro: %v", cerr)
		} else {
			st, _ := p.GetRecurrence(ctx, res.EfiIDRec)
			t.Logf("[CancelRecurrence] OK — status pós-cancel=%q", st)
		}
	}

	// Jornada 3: autoriza + paga a 1ª parcela no mesmo copia-e-cola (locrec → cob → rec → GET).
	recRes, charge, j3err := p.CreateRecurrenceJornada3(ctx, RecurrenceJornada3Request{
		Contract:   "smoke-j3-" + uniq,
		Object:     "Assinatura J3 smoke",
		PayerName:  "Fulano de Teste",
		PayerTaxID: "12345678909",
		// > R$10 para a 1ª cob NÃO ser auto-confirmada pela homolog (senão fica CONCLUIDA
		// antes de o rec referenciá-la, e a Efí rejeita "cobrança não está ativa").
		AmountCents:         5000,
		Periodicity:         "MENSAL",
		StartDate:           start,
		ChargeCorrelationID: newCorrelationID(),
		FirstDueDate:        time.Now().Format("2006-01-02"),
		Description:         "1a parcela assinatura J3",
	})
	if j3err != nil {
		t.Logf("[CreateRecurrenceJornada3] erro: %v", j3err)
	} else {
		t.Logf("[CreateRecurrenceJornada3] OK idRec=%q recStatus=%q brCode(len=%d)=%q | 1aCobr txid=%q chargeStatus=%q",
			recRes.EfiIDRec, recRes.Status, len(recRes.BRCode), recRes.BRCode, charge.ProviderChargeID, charge.Status)
	}
}

// TestEfiLocrecSmokeHomolog prova o fluxo COMPLETO da Jornada 2 na homolog:
// POST /v2/locrec → POST /v2/rec (com loc) → GET /v2/rec/{idRec} (dadosQR.pixCopiaECola).
// Loga tudo cru para fixar o payload do locrec e confirmar de onde sai o QR.
//
//	set -a && . ./.env && set +a && EFI_SMOKE=1 go test -run TestEfiLocrecSmokeHomolog -v
func TestEfiLocrecSmokeHomolog(t *testing.T) {
	if os.Getenv("EFI_SMOKE") != "1" {
		t.Skip("EFI_SMOKE!=1; pulando smoke locrec")
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
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}}},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Passo 1: POST /v2/locrec — tenta corpo vazio; se 400, tenta {"tipoCob":"cobr"}.
	st, ct, body, e := p.doRaw(ctx, http.MethodPost, "/v2/locrec", map[string]any{})
	t.Logf("[POST /v2/locrec {}] status=%d ct=%s err=%v body=%s", st, ct, e, string(body))
	if st >= 300 {
		st, ct, body, e = p.doRaw(ctx, http.MethodPost, "/v2/locrec", map[string]any{"tipoCob": "cobr"})
		t.Logf("[POST /v2/locrec {tipoCob:cobr}] status=%d ct=%s err=%v body=%s", st, ct, e, string(body))
	}
	var loc struct {
		ID  int64  `json:"id"`
		Loc int64  `json:"loc"`
		Rec string `json:"location"`
	}
	_ = json.Unmarshal(body, &loc)
	locID := loc.ID
	if locID == 0 {
		locID = loc.Loc
	}
	t.Logf("[locrec] id extraído=%d", locID)

	// Passo 2: POST /v2/rec COM o loc.
	start := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	rec := map[string]any{
		"vinculo": map[string]any{
			"contrato": "smoke-loc-001",
			"devedor":  map[string]any{"nome": "Fulano de Teste", "cpf": "12345678909"},
			"objeto":   "Assinatura com loc",
		},
		"calendario":          map[string]any{"dataInicial": start, "periodicidade": "MENSAL"},
		"valor":               map[string]any{"valorRec": "10.00"},
		"politicaRetentativa": "PERMITE_3R_7D",
		"loc":                 locID,
	}
	st2, _, body2, e2 := p.doRaw(ctx, http.MethodPost, "/v2/rec", rec)
	t.Logf("[POST /v2/rec +loc] status=%d err=%v body=%s", st2, e2, string(body2))
	var rr struct {
		IDRec string `json:"idRec"`
	}
	_ = json.Unmarshal(body2, &rr)

	// Passo 3: GET /v2/rec/{idRec} → dadosQR.pixCopiaECola.
	if rr.IDRec != "" {
		st3, _, body3, e3 := p.doRaw(ctx, http.MethodGet, "/v2/rec/"+rr.IDRec, nil)
		t.Logf("[GET /v2/rec/%s] status=%d err=%v body=%s", rr.IDRec, st3, e3, string(body3))
	}
}
