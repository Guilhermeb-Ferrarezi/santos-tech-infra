package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// ── helpers de teste para cartão ─────────────────────────────────────────────

// newTestCobrCard monta um efiCobrancas apontando para um servidor de teste.
// Reutiliza o construtor do boleto.
func newTestCobrCard(base string) *efiCobrancas {
	return newTestCobr(base)
}

// cobrMux monta um http.ServeMux com os endpoints mínimos da API Cobranças
// (authorize + rota configurável) para testes de cartão.
func cobrMux(t *testing.T, extraRoutes func(mux *http.ServeMux)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/authorize", func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); !ok {
			t.Error("authorize sem Basic Auth")
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-test", "expires_in": 600})
	})
	if extraRoutes != nil {
		extraRoutes(mux)
	}
	return httptest.NewServer(mux)
}

// ── Testes de efi_cobrancas (Installments + ChargeCardOneStep + RefundCard) ──

func TestInstallments(t *testing.T) {
	srv := cobrMux(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/v1/installments", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("método esperado GET, veio %s", r.Method)
			}
			brand := r.URL.Query().Get("brand")
			total := r.URL.Query().Get("total")
			if brand == "" || total == "" {
				t.Error("brand e total obrigatórios na query")
			}
			json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{
					"installments": []map[string]any{
						{"installment": 1, "value": 10000, "interest_rate": 0.0},
						{"installment": 2, "value": 5100, "interest_rate": 1.99},
						{"installment": 3, "value": 3450, "interest_rate": 2.99},
					},
				},
			})
		})
	})
	defer srv.Close()

	p := newTestCobrCard(srv.URL)
	insts, err := p.Installments(context.Background(), "visa", 10000)
	if err != nil {
		t.Fatalf("Installments: %v", err)
	}
	if len(insts) != 3 {
		t.Fatalf("esperado 3 parcelas, veio %d", len(insts))
	}
	if insts[0].Number != 1 || insts[0].ValueCents != 10000 || insts[0].InterestPercent != 0.0 {
		t.Fatalf("parcela 1 inesperada: %+v", insts[0])
	}
	if insts[1].Number != 2 || insts[1].ValueCents != 5100 || insts[1].InterestPercent != 1.99 {
		t.Fatalf("parcela 2 inesperada: %+v", insts[1])
	}
}

func TestChargeCardOneStep(t *testing.T) {
	var called atomic.Bool
	srv := cobrMux(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/v1/charge/one-step", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("método esperado POST, veio %s", r.Method)
			}
			// Valida que o payload NÃO contém PAN/CVV.
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error("corpo inválido")
			}
			payment, _ := body["payment"].(map[string]any)
			if payment == nil {
				t.Error("payment ausente no corpo")
			}
			cc, _ := payment["credit_card"].(map[string]any)
			if cc == nil {
				t.Error("credit_card ausente no corpo")
			}
			// payment_token deve estar presente; PAN/CVV não.
			if cc["payment_token"] == nil {
				t.Error("payment_token ausente no credit_card")
			}
			if cc["card_number"] != nil || cc["cvv"] != nil || cc["number"] != nil {
				t.Error("PAN/CVV não deve ir ao gateway")
			}
			called.Store(true)
			json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{
					"charge_id": 99001,
					"status":    "waiting",
					"message":   "",
				},
			})
		})
	})
	defer srv.Close()

	p := newTestCobrCard(srv.URL)
	result, err := p.ChargeCardOneStep(context.Background(), CardChargeInput{
		PaymentToken: "pt_test_abc123",
		Installments: 1,
		AmountCents:  5000,
		Description:  "Teste cartão",
		Customer: CardCustomer{
			Name:  "João Silva",
			TaxID: "12345678901",
			Email: "joao@example.com",
		},
		BillingAddress: CardBillingAddress{
			Street:       "Rua A",
			Number:       "100",
			Neighborhood: "Centro",
			ZipCode:      "01310100",
			City:         "São Paulo",
			State:        "SP",
		},
	})
	if err != nil {
		t.Fatalf("ChargeCardOneStep: %v", err)
	}
	if !called.Load() {
		t.Fatal("endpoint /v1/charge/one-step não foi chamado")
	}
	if result.ChargeID != "99001" {
		t.Fatalf("ChargeID esperado 99001, veio %q", result.ChargeID)
	}
	if result.Status != "waiting" {
		t.Fatalf("Status esperado waiting, veio %q", result.Status)
	}
}

func TestRefundCard(t *testing.T) {
	var refundCalled atomic.Bool
	srv := cobrMux(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/v1/charge/card/99001/refund", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("método esperado POST, veio %s", r.Method)
			}
			refundCalled.Store(true)
			json.NewEncoder(w).Encode(map[string]any{"code": 200})
		})
	})
	defer srv.Close()

	p := newTestCobrCard(srv.URL)
	if err := p.RefundCard(context.Background(), "99001", nil); err != nil {
		t.Fatalf("RefundCard: %v", err)
	}
	if !refundCalled.Load() {
		t.Fatal("endpoint de estorno não foi chamado")
	}
}

func TestRefundCardParcial(t *testing.T) {
	var got map[string]any
	srv := cobrMux(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/v1/charge/card/77/refund", func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&got)
			json.NewEncoder(w).Encode(map[string]any{"code": 200})
		})
	})
	defer srv.Close()

	p := newTestCobrCard(srv.URL)
	amount := int64(2500)
	if err := p.RefundCard(context.Background(), "77", &amount); err != nil {
		t.Fatalf("RefundCard parcial: %v", err)
	}
	if v, ok := got["value"].(float64); !ok || int64(v) != 2500 {
		t.Fatalf("value esperado 2500, veio %v", got["value"])
	}
}

func TestMapCardStatus(t *testing.T) {
	cases := []struct{ in, want string }{
		{"paid", "paid"},
		{"approved", "paid"},
		{"new", "waiting"},
		{"waiting", "waiting"},
		{"unpaid", "unpaid"},
		{"canceled", "canceled"},
		{"refunded", "refunded"},
		{"unknown_status", "unknown_status"},
	}
	for _, tc := range cases {
		if got := mapCardStatus(tc.in); got != tc.want {
			t.Errorf("mapCardStatus(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ── Testes de handlers_cards ──────────────────────────────────────────────────

// buildCardServer monta um Server com efiCobr apontando para srv e store=nil (sem DB).
func buildCardServer(efiBase string) *Server {
	cobr := newTestCobrCard(efiBase)
	return &Server{efiCobr: cobr}
}

func TestHandleCreateCardCharge_PaymentToken(t *testing.T) {
	var chargeOneStepCalled atomic.Bool
	srv := cobrMux(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/v1/charge/one-step", func(w http.ResponseWriter, r *http.Request) {
			chargeOneStepCalled.Store(true)
			json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{"charge_id": 12345, "status": "waiting", "message": ""},
			})
		})
	})
	defer srv.Close()

	s := buildCardServer(srv.URL)
	body := `{
		"paymentToken": "pt_sandbox_xyz",
		"installments": 1,
		"amountCents": 9990,
		"customer": {"name":"Maria", "taxId":"94271564656", "email":"maria@example.com"},
		"billingAddress": {"street":"Rua B","number":"2","neighborhood":"Bairro","zipCode":"01310200","city":"SP","state":"SP"}
	}`
	req := httptest.NewRequest("POST", "/charges/card", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleCreateCardCharge(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("esperado 201, veio %d: %s", w.Code, w.Body.String())
	}
	if !chargeOneStepCalled.Load() {
		t.Fatal("endpoint Efí não foi chamado")
	}
	var out Charge
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Method != "card" {
		t.Fatalf("method esperado card, veio %q", out.Method)
	}
	if out.AmountCents != 9990 {
		t.Fatalf("amountCents esperado 9990, veio %d", out.AmountCents)
	}
	if out.Status != "waiting" {
		t.Fatalf("status esperado waiting, veio %q", out.Status)
	}
}

func TestHandleCreateCardCharge_RejectPAN(t *testing.T) {
	// Garante que o handler rejeita qualquer corpo com campo "number" (PAN) ou "cvv".
	srv := cobrMux(t, nil) // sem rota extra — a Efí NÃO deve ser chamada
	defer srv.Close()

	s := buildCardServer(srv.URL)

	sensitivePayloads := []struct {
		name string
		body string
	}{
		{
			name: "number (PAN)",
			body: `{"paymentToken":"pt_x","number":"4111111111111111","amountCents":100,"customer":{"name":"X","taxId":"1","email":"x@x.com"},"billingAddress":{}}`,
		},
		{
			name: "cvv",
			body: `{"paymentToken":"pt_x","cvv":"123","amountCents":100,"customer":{"name":"X","taxId":"1","email":"x@x.com"},"billingAddress":{}}`,
		},
		{
			name: "card_number",
			body: `{"paymentToken":"pt_x","card_number":"4111111111111111","amountCents":100,"customer":{"name":"X","taxId":"1","email":"x@x.com"},"billingAddress":{}}`,
		},
	}

	for _, tc := range sensitivePayloads {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/charges/card", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.handleCreateCardCharge(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("esperado 400 para corpo com %s, veio %d: %s", tc.name, w.Code, w.Body.String())
			}
			var out map[string]string
			json.Unmarshal(w.Body.Bytes(), &out)
			if out["code"] != "sensitive_fields" {
				t.Fatalf("code esperado sensitive_fields, veio %q", out["code"])
			}
			// O corpo sensível NÃO deve aparecer no log — verificamos apenas que o handler
			// retornou 400 e não chamou o gateway.
		})
	}
}

func TestHandleCreateCardCharge_MissingPaymentToken(t *testing.T) {
	srv := cobrMux(t, nil)
	defer srv.Close()

	s := buildCardServer(srv.URL)
	body := `{"amountCents":1000,"customer":{"name":"X","taxId":"1","email":"x@x.com"},"billingAddress":{}}`
	req := httptest.NewRequest("POST", "/charges/card", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCreateCardCharge(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 sem payment_token, veio %d", w.Code)
	}
}

func TestHandleCreateCardCharge_StatusUnpaid(t *testing.T) {
	// Gateway retorna unpaid (recusado); handler deve criar cobrança com refusal.
	srv := cobrMux(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/v1/charge/one-step", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{"charge_id": 55001, "status": "unpaid", "message": "Saldo insuficiente"},
			})
		})
	})
	defer srv.Close()

	s := buildCardServer(srv.URL)
	body := `{
		"paymentToken": "pt_test",
		"installments": 1,
		"amountCents": 5000,
		"customer": {"name":"Pedro","taxId":"11122233344","email":"pedro@example.com"},
		"billingAddress": {"street":"R","number":"1","neighborhood":"B","zipCode":"01234000","city":"SP","state":"SP"}
	}`
	req := httptest.NewRequest("POST", "/charges/card", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCreateCardCharge(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("esperado 201 mesmo para unpaid, veio %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Status  string           `json:"status"`
		Refusal *CardRefusalInfo `json:"refusal"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Status != "unpaid" {
		t.Fatalf("status esperado unpaid, veio %q", out.Status)
	}
	if out.Refusal == nil {
		t.Fatal("refusal deveria estar preenchido para status unpaid")
	}
	if out.Refusal.Reason != "Saldo insuficiente" {
		t.Fatalf("reason esperado 'Saldo insuficiente', veio %q", out.Refusal.Reason)
	}
	if !out.Refusal.Retry {
		t.Fatal("retry deveria ser true para unpaid")
	}
}

func TestHandleGetInstallments(t *testing.T) {
	srv := cobrMux(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/v1/installments", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{
					"installments": []map[string]any{
						{"installment": 1, "value": 8000, "interest_rate": 0.0},
					},
				},
			})
		})
	})
	defer srv.Close()

	s := buildCardServer(srv.URL)
	req := httptest.NewRequest("GET", "/installments?brand=mastercard&total=8000", nil)
	w := httptest.NewRecorder()
	s.handleGetInstallments(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var out []Installment
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("esperado 1 parcela, veio %d", len(out))
	}
	if out[0].Number != 1 || out[0].ValueCents != 8000 {
		t.Fatalf("parcela inesperada: %+v", out[0])
	}
}

func TestHandleGetInstallments_MissingBrand(t *testing.T) {
	srv := cobrMux(t, nil)
	defer srv.Close()

	s := buildCardServer(srv.URL)
	req := httptest.NewRequest("GET", "/installments?total=5000", nil)
	w := httptest.NewRecorder()
	s.handleGetInstallments(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 sem brand, veio %d", w.Code)
	}
}

func TestHandleGetInstallments_InvalidTotal(t *testing.T) {
	srv := cobrMux(t, nil)
	defer srv.Close()

	s := buildCardServer(srv.URL)
	req := httptest.NewRequest("GET", "/installments?brand=visa&total=abc", nil)
	w := httptest.NewRecorder()
	s.handleGetInstallments(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para total inválido, veio %d", w.Code)
	}
}

func TestHandleRefundCard_StoreNil(t *testing.T) {
	// Com store=nil, o handler deve retornar 503 store_unavailable (Fix 3).
	srv := cobrMux(t, nil) // gateway NÃO deve ser chamado
	defer srv.Close()

	s := buildCardServer(srv.URL) // store=nil
	req := httptest.NewRequest("POST", "/charges/card/42/refund", strings.NewReader("{}"))
	req.SetPathValue("id", "42")
	w := httptest.NewRecorder()
	s.handleRefundCard(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("esperado 503 com store=nil, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "store_unavailable" {
		t.Fatalf("code esperado store_unavailable, veio %q", out["code"])
	}
}

func TestHandleCardCharge_EfiCobrNil(t *testing.T) {
	// Sem efiCobr configurado → 503
	s := &Server{}
	req := httptest.NewRequest("POST", "/charges/card", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	s.handleCreateCardCharge(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("esperado 503 sem efiCobr, veio %d", w.Code)
	}
}

// hasSensitiveCardFields — testes unitários (inclui varredura aninhada)
func TestHasSensitiveCardFields(t *testing.T) {
	yes := []string{
		`{"number":"4111111111111111","paymentToken":"pt"}`,
		`{"cvv":"123","paymentToken":"pt"}`,
		`{"card_number":"4111111111111111"}`,
		`{"pan":"4111"}`,
		// aninhados — Fix 1: varredura recursiva
		`{"paymentToken":"pt_x","card":{"number":"4111111111111111","cvv":"123"}}`,
		`{"payment":{"card_number":"4111"}}`,
		`{"items":[{"number":"4111"}]}`,
		`{"paymentToken":"pt","extra":{"nested":{"cvc":"999"}}}`,
	}
	no := []string{
		`{"paymentToken":"pt","installments":1}`,
		`{"customer":{"name":"X"},"billingAddress":{}}`,
		`{}`,
	}
	for _, s := range yes {
		if !hasSensitiveCardFields([]byte(s)) {
			t.Errorf("esperava true para %s", s)
		}
	}
	for _, s := range no {
		if hasSensitiveCardFields([]byte(s)) {
			t.Errorf("esperava false para %s", s)
		}
	}
}

// ── Testes das correções de revisão ──────────────────────────────────────────

// [Fix 4] handleRefundCard: ProviderChargeID vazio → 409 no_provider_id.
func TestHandleRefundCard_NoProviderID(t *testing.T) {
	srv := cobrMux(t, nil) // gateway NÃO deve ser chamado
	defer srv.Close()

	fakeStore := &fakeStoreForRefund{charge: &Charge{
		ID:               55,
		Method:           "card",
		Status:           "paid",
		ProviderChargeID: "", // vazio — deve gerar 409 no_provider_id
		CorrelationID:    "corr-55",
	}}
	s := &Server{efiCobr: newTestCobrCard(srv.URL), refund: fakeStore}
	req := httptest.NewRequest("POST", "/charges/card/55/refund", strings.NewReader("{}"))
	req.SetPathValue("id", "55")
	w := httptest.NewRecorder()
	s.handleRefundCard(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("[Fix4] esperado 409 no_provider_id, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "no_provider_id" {
		t.Fatalf("[Fix4] code esperado no_provider_id, veio %q", out["code"])
	}
}

// [Fix 8] handleRefundCard: já refunded → 409 already_refunded.
func TestHandleRefundCard_AlreadyRefunded(t *testing.T) {
	srv := cobrMux(t, nil)
	defer srv.Close()

	fakeStore := &fakeStoreForRefund{charge: &Charge{
		ID:               66,
		Method:           "card",
		Status:           "refunded", // já estornado
		ProviderChargeID: "ch_66",
		CorrelationID:    "corr-66",
	}}
	s := &Server{efiCobr: newTestCobrCard(srv.URL), refund: fakeStore}
	req := httptest.NewRequest("POST", "/charges/card/66/refund", strings.NewReader("{}"))
	req.SetPathValue("id", "66")
	w := httptest.NewRecorder()
	s.handleRefundCard(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("[Fix8] esperado 409 already_refunded, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "already_refunded" {
		t.Fatalf("[Fix8] code esperado already_refunded, veio %q", out["code"])
	}
}

// [Fix 8] handleRefundCard happy path: gateway OK → chama MarkChargeRefunded.
func TestHandleRefundCard_MarksRefunded(t *testing.T) {
	var refundCalled atomic.Bool
	srv := cobrMux(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/v1/charge/card/ch_77/refund", func(w http.ResponseWriter, r *http.Request) {
			refundCalled.Store(true)
			json.NewEncoder(w).Encode(map[string]any{"code": 200})
		})
	})
	defer srv.Close()

	fakeStore := &fakeStoreForRefund{charge: &Charge{
		ID:               77,
		Method:           "card",
		Status:           "paid",
		ProviderChargeID: "ch_77",
		CorrelationID:    "corr-77",
	}}
	s := &Server{efiCobr: newTestCobrCard(srv.URL), refund: fakeStore}
	req := httptest.NewRequest("POST", "/charges/card/77/refund", strings.NewReader("{}"))
	req.SetPathValue("id", "77")
	w := httptest.NewRecorder()
	s.handleRefundCard(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("[Fix8] esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	if !refundCalled.Load() {
		t.Fatal("[Fix8] endpoint de estorno não foi chamado no gateway")
	}
	if fakeStore.refundedCorrelationID != "corr-77" {
		t.Fatalf("[Fix8] MarkChargeRefunded não foi chamado com correlationID correto, veio %q",
			fakeStore.refundedCorrelationID)
	}
}

// [Fix 6] handleGetInstallments rejeita bandeira inválida → 400 invalid_brand.
func TestHandleGetInstallments_InvalidBrand(t *testing.T) {
	srv := cobrMux(t, nil) // gateway NÃO deve ser chamado
	defer srv.Close()

	s := buildCardServer(srv.URL)
	req := httptest.NewRequest("GET", "/installments?brand=bitcoin&total=5000", nil)
	w := httptest.NewRecorder()
	s.handleGetInstallments(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("[Fix6] esperado 400 para bandeira inválida, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "invalid_brand" {
		t.Fatalf("[Fix6] code esperado invalid_brand, veio %q", out["code"])
	}
}

// [Fix 6] bandeiras válidas passam sem 400 invalid_brand (a Efí pode dar erro depois, ok).
func TestHandleGetInstallments_ValidBrands(t *testing.T) {
	for _, brand := range []string{"visa", "mastercard", "elo", "amex", "hipercard"} {
		brand := brand
		t.Run(brand, func(t *testing.T) {
			srv := cobrMux(t, func(mux *http.ServeMux) {
				mux.HandleFunc("/v1/installments", func(w http.ResponseWriter, r *http.Request) {
					json.NewEncoder(w).Encode(map[string]any{
						"code": 200,
						"data": map[string]any{"installments": []any{}},
					})
				})
			})
			defer srv.Close()
			s := buildCardServer(srv.URL)
			req := httptest.NewRequest("GET", "/installments?brand="+brand+"&total=5000", nil)
			w := httptest.NewRecorder()
			s.handleGetInstallments(w, req)
			if w.Code == http.StatusBadRequest {
				var out map[string]string
				json.Unmarshal(w.Body.Bytes(), &out)
				if out["code"] == "invalid_brand" {
					t.Fatalf("[Fix6] brand %q válida foi rejeitada como inválida", brand)
				}
			}
		})
	}
}

// fakeStoreForRefund implementa a interface mínima para handleRefundCard.
type fakeStoreForRefund struct {
	charge                *Charge
	refundedCorrelationID string
}

func (f *fakeStoreForRefund) GetCharge(_ context.Context, _ int64) (*Charge, error) {
	if f.charge == nil {
		return nil, nil
	}
	c := *f.charge
	return &c, nil
}

func (f *fakeStoreForRefund) MarkChargeRefunded(_ context.Context, correlationID string) error {
	f.refundedCorrelationID = correlationID
	return nil
}

// TestHandleCreateCardCharge_RejectNestedPAN verifica que PAN aninhado em "card.number"
// também é rejeitado com 400 sensitive_fields (Fix 1).
func TestHandleCreateCardCharge_RejectNestedPAN(t *testing.T) {
	srv := cobrMux(t, nil) // Efí NÃO deve ser chamada
	defer srv.Close()

	s := buildCardServer(srv.URL)
	body := `{"paymentToken":"pt_x","card":{"number":"4111111111111111","cvv":"123"},"amountCents":100,"customer":{"name":"X","taxId":"1","email":"x@x.com"},"billingAddress":{}}`
	req := httptest.NewRequest("POST", "/charges/card", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleCreateCardCharge(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para PAN aninhado, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "sensitive_fields" {
		t.Fatalf("code esperado sensitive_fields, veio %q", out["code"])
	}
}
