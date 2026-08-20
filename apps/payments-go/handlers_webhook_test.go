package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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
	r := httptest.NewRequest("POST", "/webhooks/efi/pix?token=wrong", strings.NewReader(`{"pix":[]}`))
	w := httptest.NewRecorder()
	s.handleWebhook(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("segredo errado deveria dar 401, veio %d", w.Code)
	}
}

func TestHandleWebhookSegredoCerto(t *testing.T) {
	s := &Server{cfg: Config{EFIWebhookSecret: "right"}, provider: efiStub{}} // store nil → 200 cedo
	r := httptest.NewRequest("POST", "/webhooks/efi/pix?token=right", strings.NewReader(`{"pix":[{"txid":"stpayAAA"}]}`))
	w := httptest.NewRecorder()
	s.handleWebhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("segredo certo deveria dar 200, veio %d", w.Code)
	}
}

// ── Testes do webhook de Cobranças (handleCobrWebhook) — cartão ───────────────

// cobrWebhookServer monta um efiCobrancas apontando para um servidor httptest que
// simula o endpoint de notificação da Efí com o status indicado.
func cobrWebhookServer(t *testing.T, token, chargeID, status string) (*httptest.Server, *efiCobrancas) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/authorize", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "tok", "expires_in": 600})
	})
	mux.HandleFunc("/v1/notification/"+token, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"code": 200,
			"data": []map[string]any{
				{
					"identifiers": map[string]any{"charge_id": func() any {
						// charge_id como número na resposta da Efí
						id := int64(0)
						for _, c := range chargeID {
							id = id*10 + int64(c-'0')
						}
						if id == 0 {
							return chargeID // fallback string
						}
						return id
					}()},
					"status": map[string]any{"current": status},
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	cobr := newTestCobr(srv.URL)
	return srv, cobr
}

// fakeCobrStore implementa o subconjunto de métodos do *Store usado pelo handleCobrWebhook.
type fakeCobrStore struct {
	marked      bool // resultado de MarkChargePaidByProviderID
	publicToken string
	markErr     error
	tokenErr    error
}

func (f *fakeCobrStore) MarkChargePaidByProviderID(_ context.Context, _ string) (bool, error) {
	return f.marked, f.markErr
}
func (f *fakeCobrStore) PublicTokenByProviderID(_ context.Context, _ string) (string, error) {
	return f.publicToken, f.tokenErr
}

// Implementações vazias das demais interfaces que o *Store satisfaz (evita type-assertions).
// handleCobrWebhook usa s.store que é *Store; para testar sem DB real, precisamos
// configurar s.efiCobr (interface leve) e deixar s.store=nil (caminho defensivo).
// A cobertura real dos caminhos de DB é feita via testes de integração do store.

// TestHandleCobrWebhook_Paid verifica que o webhook de Cobranças, ao receber
// notification=<token>, busca o status na Efí e responde 200. Com store=nil
// (guarda defensiva), o handler retorna 200 sem tocar no DB.
func TestHandleCobrWebhook_Paid(t *testing.T) {
	const secret = "secret-cobr-paid"
	const tok = "notif-tok-paid"
	const chargeID = "12345"
	srv, cobr := cobrWebhookServer(t, tok, chargeID, "paid")
	defer srv.Close()

	s := &Server{
		cfg:      Config{EFIWebhookSecret: secret},
		efiCobr:  cobr,
		store:    nil, // guarda defensiva: 200 sem DB
		provider: efiStub{},
	}
	body := `{"notification":"` + tok + `"}`
	req := httptest.NewRequest("POST", "/webhooks/efi/cobr?token="+secret, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCobrWebhook(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleCobrWebhook_FormEncoded cobre o formato REAL da Efí: a API de Cobranças
// POSTa "notification=<token>" como application/x-www-form-urlencoded (não JSON).
// Regressão do 400 "Payload inválido" que rejeitava a notificação verdadeira.
func TestHandleCobrWebhook_FormEncoded(t *testing.T) {
	const secret = "secret-cobr-form"
	const tok = "notif-tok-form"
	srv, cobr := cobrWebhookServer(t, tok, "98765", "paid")
	defer srv.Close()

	s := &Server{cfg: Config{EFIWebhookSecret: secret}, efiCobr: cobr, store: nil, provider: efiStub{}}
	body := "notification=" + tok
	req := httptest.NewRequest("POST", "/webhooks/efi/cobr?token="+secret, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleCobrWebhook(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("form-urlencoded deveria retornar 200, veio %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleCobrWebhook_Unpaid verifica que status "unpaid" não causa erro (200 OK)
// — a charge permanece pendente; o log registra o evento.
func TestHandleCobrWebhook_Unpaid(t *testing.T) {
	const secret = "secret-cobr-unpaid"
	const tok = "notif-tok-unpaid"
	srv, cobr := cobrWebhookServer(t, tok, "55001", "unpaid")
	defer srv.Close()

	s := &Server{cfg: Config{EFIWebhookSecret: secret}, efiCobr: cobr, store: nil, provider: efiStub{}}
	body := `{"notification":"` + tok + `"}`
	req := httptest.NewRequest("POST", "/webhooks/efi/cobr?token="+secret, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCobrWebhook(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status unpaid deveria retornar 200, veio %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleCobrWebhook_TokenBusca verifica que o handler parseia o token de notificação
// do corpo do webhook e responde 200. Com store=nil (guarda defensiva) o handler retorna
// 200 sem chamar a Efí — o importante é que o campo "notification" seja extraído do corpo
// e outros campos arbitrários do webhook (que a Efí pode mandar) sejam ignorados sem erro.
func TestHandleCobrWebhook_TokenBusca(t *testing.T) {
	const secret = "secret-cobr-busca"
	s := &Server{cfg: Config{EFIWebhookSecret: secret}, efiCobr: nil, store: nil, provider: efiStub{}}
	body := `{"notification":"meu-token-unico"}`
	req := httptest.NewRequest("POST", "/webhooks/efi/cobr?token="+secret, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCobrWebhook(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleCobrWebhook_SegredoInvalido verifica que segredo errado retorna 401.
func TestHandleCobrWebhook_SegredoInvalido(t *testing.T) {
	s := &Server{cfg: Config{Production: true, EFIWebhookSecret: "correto"}, provider: efiStub{}}
	req := httptest.NewRequest("POST", "/webhooks/efi/cobr?token=errado", strings.NewReader(`{"notification":"x"}`))
	w := httptest.NewRecorder()
	s.handleCobrWebhook(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("esperado 401 com segredo errado, veio %d", w.Code)
	}
}

// TestHandleCobrWebhook_CorpoVazio verifica que corpo vazio (registro do webhook) retorna 200.
func TestHandleCobrWebhook_CorpoVazio(t *testing.T) {
	const secret = "secret-cobr-vazio"
	s := &Server{cfg: Config{EFIWebhookSecret: secret}, provider: efiStub{}}
	req := httptest.NewRequest("POST", "/webhooks/efi/cobr?token="+secret, strings.NewReader(""))
	w := httptest.NewRecorder()
	s.handleCobrWebhook(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("corpo vazio deveria dar 200, veio %d: %s", w.Code, w.Body.String())
	}
}

// fakeCobrWHStore implementa cobrWebhookStore para testes sem DB, reproduzindo a
// máquina de estados 'pending'/'done' do pay_webhook_events.
type fakeCobrWHStore struct {
	mu     sync.Mutex
	events map[string]string // id → "pending" | "done"

	markPaidCalls atomic.Int32
	markPaidErr   error
}

func (f *fakeCobrWHStore) ClaimWebhookEvent(_ context.Context, id, _ string, _ []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.events == nil {
		f.events = map[string]string{}
	}
	if f.events[id] == "done" {
		return false, nil
	}
	f.events[id] = "pending"
	return true, nil
}

func (f *fakeCobrWHStore) MarkWebhookDone(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.events == nil {
		f.events = map[string]string{}
	}
	f.events[id] = "done"
	return nil
}

func (f *fakeCobrWHStore) eventState(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events[id]
}

func (f *fakeCobrWHStore) MarkChargePaidByProviderID(_ context.Context, _ string) (bool, error) {
	f.markPaidCalls.Add(1)
	if f.markPaidErr != nil {
		return false, f.markPaidErr
	}
	return true, nil
}

func (f *fakeCobrWHStore) PublicTokenByProviderID(_ context.Context, _ string) (string, error) {
	return "", nil
}

// TestCobrWebhook_FalhaNaEfiNaoConsomeEvento verifica o núcleo do processamento em
// duas fases: se a consulta à Efí falha, o evento NÃO é encerrado e a reentrega
// processa de verdade. Antes o ID era consumido antes do efeito, então o 502 pedia
// um retry que era no-op garantido — o pagamento sumia.
func TestCobrWebhook_FalhaNaEfiNaoConsomeEvento(t *testing.T) {
	var falhar atomic.Bool
	falhar.Store(true)
	var notifCalls atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/authorize", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "tok", "expires_in": 600})
	})
	mux.HandleFunc("/v1/notification/notif-falha", func(w http.ResponseWriter, r *http.Request) {
		notifCalls.Add(1)
		if falhar.Load() {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": []any{
			map[string]any{
				"identifiers": map[string]any{"charge_id": 4242},
				"status":      map[string]any{"current": "paid"},
			},
		}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	const secret = "secret-cobr-retry"
	st := &fakeCobrWHStore{}
	s := &Server{cfg: Config{EFIWebhookSecret: secret}, efiCobr: newTestCobr(srv.URL), cobrWH: st, provider: efiStub{}}
	body := `notification=notif-falha`

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/webhooks/efi/cobr?token="+secret, strings.NewReader(body))
		w := httptest.NewRecorder()
		s.handleCobrWebhook(w, req)
		return w
	}

	// 1ª entrega: a Efí falha → 5xx e o evento continua 'pending' (NÃO consumido).
	if w := post(); w.Code < 500 {
		t.Fatalf("falha na Efí deveria devolver 5xx, veio %d: %s", w.Code, w.Body.String())
	}
	if got := st.eventState("notif-falha"); got != "pending" {
		t.Fatalf("evento deveria continuar 'pending' após a falha, está %q", got)
	}
	if st.markPaidCalls.Load() != 0 {
		t.Fatalf("nada deveria ter sido marcado pago na falha, foram %d", st.markPaidCalls.Load())
	}

	// 2ª entrega (reentrega da Efí), agora com sucesso: o pagamento é aplicado.
	falhar.Store(false)
	if w := post(); w.Code != http.StatusOK {
		t.Fatalf("reentrega deveria dar 200, veio %d: %s", w.Code, w.Body.String())
	}
	if st.markPaidCalls.Load() != 1 {
		t.Fatalf("reentrega deveria ter marcado a cobrança paga 1 vez, foram %d", st.markPaidCalls.Load())
	}
	if got := st.eventState("notif-falha"); got != "done" {
		t.Fatalf("evento deveria estar 'done' após o sucesso, está %q", got)
	}

	// 3ª entrega: agora sim é no-op (idempotência preservada).
	if w := post(); w.Code != http.StatusOK {
		t.Fatalf("3ª entrega deveria dar 200, veio %d", w.Code)
	}
	if st.markPaidCalls.Load() != 1 {
		t.Fatalf("3ª entrega não deveria remarcar como paga, foram %d", st.markPaidCalls.Load())
	}
	if notifCalls.Load() != 2 {
		t.Fatalf("a Efí deveria ter sido consultada 2 vezes (falha + reentrega), foram %d", notifCalls.Load())
	}
}

// TestCobrWebhookIdempotencia_GuardaDefensiva verifica que com store=nil a guarda
// defensiva retorna 200 sem chamar GetCobrancaNotification — logo replays também são
// seguros quando o servidor reinicia com store=nil (Fix 2, caminho defensivo).
func TestCobrWebhookIdempotencia_GuardaDefensiva(t *testing.T) {
	var notifCalled atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/authorize", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "tok", "expires_in": 600})
	})
	mux.HandleFunc("/v1/notification/tok-idem", func(w http.ResponseWriter, r *http.Request) {
		notifCalled.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": []any{}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cobr := newTestCobr(srv.URL)

	const secret = "secret-cobr-idem"
	// store=nil → guarda defensiva retorna 200 sem chamar efiCobr.GetCobrancaNotification.
	s := &Server{cfg: Config{EFIWebhookSecret: secret}, efiCobr: cobr, store: nil, provider: efiStub{}}
	body := `{"notification":"tok-idem"}`

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/webhooks/efi/cobr?token="+secret, strings.NewReader(body))
		w := httptest.NewRecorder()
		s.handleCobrWebhook(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("iter %d: esperado 200, veio %d: %s", i, w.Code, w.Body.String())
		}
	}
	if notifCalled.Load() != 0 {
		t.Fatalf("GetCobrancaNotification não deveria ser chamado com store=nil, foi %d vez(es)", notifCalled.Load())
	}
}

// ── Testes da Correção 1: confirmação de status na Efí antes de marcar paga ──

// efiProviderStub é um PaymentProvider configurável para testes de GetCharge.
type efiProviderStub struct {
	getChargeStatus string // status que GetCharge vai retornar
	getChargeCalled atomic.Int32
}

func (p *efiProviderStub) CreateCharge(_ context.Context, _ ChargeRequest) (ChargeResult, error) {
	return ChargeResult{}, nil
}
func (p *efiProviderStub) GetCharge(_ context.Context, _ string) (ChargeResult, error) {
	p.getChargeCalled.Add(1)
	return ChargeResult{Status: p.getChargeStatus}, nil
}
func (p *efiProviderStub) ParseWebhook(_ map[string][]string, body []byte) ([]WebhookEvent, error) {
	if strings.Contains(string(body), "stpay") {
		return []WebhookEvent{{ID: "EV1", Type: "CHARGE_PAID", CorrelationID: "stpayAAA"}}, nil
	}
	return nil, nil
}

// fakePixWHStore implementa pixWebhookStore para testes sem DB. Reproduz a máquina
// de estados real do pay_webhook_events: reivindicado ('pending') vs concluído ('done').
type fakePixWHStore struct {
	mu     sync.Mutex
	events map[string]string // id → "pending" | "done"

	markPaidCalled atomic.Int32
	// markPaidErr, quando != nil, faz MarkChargePaid falhar (simula erro de banco).
	markPaidErr error
	// captura de SetWithdrawalStatus (ramo PAYOUT_RESULT).
	wdStatusCalls atomic.Int32
	lastWDIDEnvio string
	lastWDE2EID   string
	lastWDStatus  string
}

func (f *fakePixWHStore) ClaimWebhookEvent(_ context.Context, id, _ string, _ []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.events == nil {
		f.events = map[string]string{}
	}
	if f.events[id] == "done" {
		return false, nil // já concluído: reentrega é no-op de propósito
	}
	f.events[id] = "pending" // 'pending' pode ser reivindicado de novo (retry)
	return true, nil
}

func (f *fakePixWHStore) MarkWebhookDone(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.events == nil {
		f.events = map[string]string{}
	}
	f.events[id] = "done"
	return nil
}

// eventState devolve o estado do evento ("" se nunca visto).
func (f *fakePixWHStore) eventState(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events[id]
}

func (f *fakePixWHStore) MarkChargePaid(_ context.Context, _ string) error {
	f.markPaidCalled.Add(1)
	return f.markPaidErr
}
func (f *fakePixWHStore) PublicTokenByCorrelation(_ context.Context, _ string) (string, error) {
	return "", nil // sem token público — efeitos secundários (SSE/email) ignorados
}
func (f *fakePixWHStore) SetWithdrawalStatus(_ context.Context, idEnvio, e2eID, status string) error {
	f.wdStatusCalls.Add(1)
	f.lastWDIDEnvio = idEnvio
	f.lastWDE2EID = e2eID
	f.lastWDStatus = status
	return nil
}

// TestHandleWebhook_ChargePaidConfirmado verifica que quando a Efí confirma status "paid",
// a charge é marcada paga no store.
func TestHandleWebhook_ChargePaidConfirmado(t *testing.T) {
	const secret = "secret-pix-confirm"
	provider := &efiProviderStub{getChargeStatus: "paid"}
	store := &fakePixWHStore{}

	s := &Server{
		cfg:      Config{EFIWebhookSecret: secret},
		provider: provider,
		pixWH:    store,
	}
	body := `{"pix":[{"txid":"stpayAAA","horario":"2026-06-21T00:00:00Z","valor":"10.00"}]}`
	req := httptest.NewRequest("POST", "/webhooks/efi/pix?token="+secret, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	if provider.getChargeCalled.Load() == 0 {
		t.Fatal("GetCharge deveria ter sido chamado para confirmar status")
	}
	if store.markPaidCalled.Load() == 0 {
		t.Fatal("MarkChargePaid deveria ter sido chamado quando status é 'paid'")
	}
}

// TestHandleWebhook_ChargePaidForjaRejeitada verifica que quando a Efí retorna status
// diferente de "paid" (ex.: "pending"), a charge NÃO é marcada paga — a forja é rejeitada.
func TestHandleWebhook_ChargePaidForjaRejeitada(t *testing.T) {
	const secret = "secret-pix-forja"
	provider := &efiProviderStub{getChargeStatus: "pending"} // Efí diz que não está paga
	store := &fakePixWHStore{}

	s := &Server{
		cfg:      Config{EFIWebhookSecret: secret},
		provider: provider,
		pixWH:    store,
	}
	body := `{"pix":[{"txid":"stpayAAA","horario":"2026-06-21T00:00:00Z","valor":"10.00"}]}`
	req := httptest.NewRequest("POST", "/webhooks/efi/pix?token="+secret, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	if provider.getChargeCalled.Load() == 0 {
		t.Fatal("GetCharge deveria ter sido chamado mesmo para forja")
	}
	if store.markPaidCalled.Load() != 0 {
		t.Fatal("MarkChargePaid NÃO deveria ter sido chamado quando status não é 'paid' (forja rejeitada)")
	}
}

// TestHandleWebhook_PayoutRealizado: webhook de Pix Envio com status REALIZADO atualiza
// o saque casado por idEnvio/e2eId para "completed" (via SetWithdrawalStatus). Usa o
// efiProvider real (ParseWebhook é parser puro, sem rede) para exercer o ramo de envio.
func TestHandleWebhook_PayoutRealizado(t *testing.T) {
	const secret = "secret-payout-realizado"
	store := &fakePixWHStore{}
	s := &Server{
		cfg:      Config{EFIWebhookSecret: secret},
		provider: &efiProvider{}, // só ParseWebhook é usado (sem rede)
		pixWH:    store,
	}
	body := `{"pix":[{"endToEndId":"E99887766","status":"REALIZADO","gnExtras":{"idEnvio":"IDENV123"}}]}`
	req := httptest.NewRequest("POST", "/webhooks/efi/pix?token="+secret, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	if store.wdStatusCalls.Load() != 1 {
		t.Fatalf("SetWithdrawalStatus chamado %d vezes, esperado 1", store.wdStatusCalls.Load())
	}
	if store.lastWDStatus != "completed" {
		t.Fatalf("status esperado completed, veio %q", store.lastWDStatus)
	}
	if store.lastWDIDEnvio != "IDENV123" || store.lastWDE2EID != "E99887766" {
		t.Fatalf("idEnvio/e2eId errados: %q/%q", store.lastWDIDEnvio, store.lastWDE2EID)
	}
	// NÃO deve marcar cobrança paga (não é pix recebido).
	if store.markPaidCalled.Load() != 0 {
		t.Fatal("MarkChargePaid NÃO deveria ser chamado num evento de payout")
	}
}

// TestHandleWebhook_PayoutNaoRealizado: status NAO_REALIZADO → "failed".
func TestHandleWebhook_PayoutNaoRealizado(t *testing.T) {
	const secret = "secret-payout-falho"
	store := &fakePixWHStore{}
	s := &Server{
		cfg:      Config{EFIWebhookSecret: secret},
		provider: &efiProvider{},
		pixWH:    store,
	}
	body := `{"pix":[{"endToEndId":"E55","status":"NAO_REALIZADO","gnExtras":{"idEnvio":"IDENV55"}}]}`
	req := httptest.NewRequest("POST", "/webhooks/efi/pix?token="+secret, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	if store.wdStatusCalls.Load() != 1 || store.lastWDStatus != "failed" {
		t.Fatalf("esperado 1 chamada com status failed; veio %d chamadas, status %q", store.wdStatusCalls.Load(), store.lastWDStatus)
	}
}

// TestHandleWebhook_PayoutEmProcessamento: status não-final (EM_PROCESSAMENTO) NÃO
// dispara atualização de status (PayoutStatus mapeia para "" → sem ação).
func TestHandleWebhook_PayoutEmProcessamento(t *testing.T) {
	const secret = "secret-payout-proc"
	store := &fakePixWHStore{}
	s := &Server{
		cfg:      Config{EFIWebhookSecret: secret},
		provider: &efiProvider{},
		pixWH:    store,
	}
	body := `{"pix":[{"endToEndId":"E77","status":"EM_PROCESSAMENTO","gnExtras":{"idEnvio":"IDENV77"}}]}`
	req := httptest.NewRequest("POST", "/webhooks/efi/pix?token="+secret, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	if store.wdStatusCalls.Load() != 0 {
		t.Fatalf("SetWithdrawalStatus NÃO deveria ser chamado em estado não-final, veio %d", store.wdStatusCalls.Load())
	}
}

// TestHandleWebhook_SecretAusente verifica que sem EFIWebhookSecret o handler
// responde 503 (fail-closed), independentemente de Production.
func TestHandleWebhook_SecretAusente(t *testing.T) {
	for _, prod := range []bool{false, true} {
		s := &Server{cfg: Config{Production: prod}, provider: efiStub{}}
		req := httptest.NewRequest("POST", "/webhooks/efi/pix", strings.NewReader(`{"pix":[]}`))
		w := httptest.NewRecorder()
		s.handleWebhook(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("Production=%v: esperado 503 sem secret, veio %d", prod, w.Code)
		}
	}
}

// TestHandleCobrWebhook_SecretAusente verifica fail-closed para o webhook cobr.
func TestHandleCobrWebhook_SecretAusente(t *testing.T) {
	s := &Server{cfg: Config{}, provider: efiStub{}}
	req := httptest.NewRequest("POST", "/webhooks/efi/cobr", strings.NewReader(`{"notification":"x"}`))
	w := httptest.NewRecorder()
	s.handleCobrWebhook(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("esperado 503 sem secret, veio %d", w.Code)
	}
}

// TestHandleWebhook_ErroDeBancoNaoConsomeEvento cobre o webhook PIX: se MarkChargePaid
// falha (erro de banco), o evento NÃO pode ser encerrado — senão a reentrega da Efí
// cai no "já visto" e o pagamento é descartado para sempre.
func TestHandleWebhook_ErroDeBancoNaoConsomeEvento(t *testing.T) {
	const secret = "secret-pix-retry"
	provider := &efiProviderStub{getChargeStatus: "paid"}
	store := &fakePixWHStore{markPaidErr: errors.New("db caiu")}

	s := &Server{cfg: Config{EFIWebhookSecret: secret}, provider: provider, pixWH: store}
	body := `{"pix":[{"txid":"stpayAAA","horario":"2026-06-21T00:00:00Z","valor":"10.00"}]}`
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/webhooks/efi/pix?token="+secret, strings.NewReader(body))
		w := httptest.NewRecorder()
		s.handleWebhook(w, req)
		return w
	}

	// 1ª entrega: banco falha → 5xx pedindo reentrega, evento continua 'pending'.
	if w := post(); w.Code < 500 {
		t.Fatalf("erro de banco deveria devolver 5xx, veio %d: %s", w.Code, w.Body.String())
	}
	if got := store.eventState("EV1"); got != "pending" {
		t.Fatalf("evento deveria continuar 'pending', está %q", got)
	}

	// 2ª entrega (reentrega), banco de volta: o pagamento é finalmente aplicado.
	store.markPaidErr = nil
	if w := post(); w.Code != http.StatusOK {
		t.Fatalf("reentrega deveria dar 200, veio %d: %s", w.Code, w.Body.String())
	}
	if store.markPaidCalled.Load() != 2 {
		t.Fatalf("MarkChargePaid deveria ter sido tentado 2 vezes (falha + reentrega), foram %d", store.markPaidCalled.Load())
	}
	if got := store.eventState("EV1"); got != "done" {
		t.Fatalf("evento deveria estar 'done' após o sucesso, está %q", got)
	}

	// 3ª entrega: no-op — a idempotência continua valendo depois do sucesso.
	if w := post(); w.Code != http.StatusOK {
		t.Fatalf("3ª entrega deveria dar 200, veio %d", w.Code)
	}
	if store.markPaidCalled.Load() != 2 {
		t.Fatalf("3ª entrega não deveria reprocessar, MarkChargePaid foi chamado %d vezes", store.markPaidCalled.Load())
	}
}

// TestHandleWebhook_StatusNaoPagoNaoEncerraEvento: a Efí ainda não confirmou o
// pagamento — responde 200 (nada a reprocessar agora), mas o evento fica 'pending'
// para que a notificação seguinte, com o status já pago, seja processada.
func TestHandleWebhook_StatusNaoPagoNaoEncerraEvento(t *testing.T) {
	const secret = "secret-pix-skip"
	provider := &efiProviderStub{getChargeStatus: "pending"}
	store := &fakePixWHStore{}
	s := &Server{cfg: Config{EFIWebhookSecret: secret}, provider: provider, pixWH: store}
	body := `{"pix":[{"txid":"stpayAAA","horario":"2026-06-21T00:00:00Z","valor":"10.00"}]}`

	req := httptest.NewRequest("POST", "/webhooks/efi/pix?token="+secret, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status não-pago deveria dar 200, veio %d", w.Code)
	}
	if store.markPaidCalled.Load() != 0 {
		t.Fatal("não deveria marcar paga com status não confirmado")
	}
	if got := store.eventState("EV1"); got != "pending" {
		t.Fatalf("evento deveria continuar 'pending' (pode virar pago depois), está %q", got)
	}
}
