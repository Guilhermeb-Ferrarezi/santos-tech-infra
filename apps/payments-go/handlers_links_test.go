package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// ── Fake store de links ───────────────────────────────────────────────────────

// fakeLinkStore é um stub em memória de paymentLinkStore para testes sem DB.
type fakeLinkStore struct {
	mu      sync.Mutex              // protege charges (testes de concorrência do cupom)
	links   map[string]*PaymentLink // indexado por public_token
	byID    map[int64]*PaymentLink
	nextID  atomic.Int64
	failOn  string // se não vazio, métodos retornam erro
	charges []*Charge
}

func newFakeLinkStore() *fakeLinkStore {
	return &fakeLinkStore{
		links: make(map[string]*PaymentLink),
		byID:  make(map[int64]*PaymentLink),
	}
}

func (f *fakeLinkStore) CreatePaymentLink(_ context.Context, l *PaymentLink) error {
	if f.failOn == "create" {
		return errors.New("db error fake")
	}
	id := f.nextID.Add(1)
	l.ID = id
	l.Status = "active"
	l.CreatedAt = time.Now()
	cp := *l
	f.links[l.PublicToken] = &cp
	f.byID[id] = &cp
	return nil
}

func (f *fakeLinkStore) GetPaymentLinkByToken(_ context.Context, token string) (*PaymentLink, error) {
	if f.failOn == "get" {
		return nil, errors.New("db error fake")
	}
	l, ok := f.links[token]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	cp := *l
	return &cp, nil
}

func (f *fakeLinkStore) GetPaymentLink(_ context.Context, id int64) (*PaymentLink, error) {
	if f.failOn == "get" {
		return nil, errors.New("db error fake")
	}
	l, ok := f.byID[id]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	cp := *l
	return &cp, nil
}

func (f *fakeLinkStore) ListPaymentLinks(_ context.Context) ([]PaymentLink, error) {
	if f.failOn == "list" {
		return nil, errors.New("db error fake")
	}
	out := make([]PaymentLink, 0, len(f.byID))
	for _, l := range f.byID {
		out = append(out, *l)
	}
	return out, nil
}

func (f *fakeLinkStore) SetPaymentLinkStatus(_ context.Context, id int64, status string) error {
	if f.failOn == "patch" {
		return errors.New("db error fake")
	}
	l, ok := f.byID[id]
	if !ok {
		return pgx.ErrNoRows
	}
	l.Status = status
	f.links[l.PublicToken].Status = status
	return nil
}

func (f *fakeLinkStore) InsertChargeWithLink(_ context.Context, c *Charge, linkID int64) error {
	if f.failOn == "insert_charge" {
		return errors.New("db error fake")
	}
	c.ID = 1
	c.Status = "pending"
	c.CreatedAt = time.Now()
	cp := *c
	cp.LinkID = &linkID
	f.mu.Lock()
	f.charges = append(f.charges, &cp)
	f.mu.Unlock()
	return nil
}

func (f *fakeLinkStore) MarkChargePaid(_ context.Context, _ string) error {
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// buildLinkServer monta um Server com o fakeLinkStore injetado.
// Injeta um rate-limiter ilimitado para evitar interferência entre testes.
func buildLinkServer(st *fakeLinkStore) *Server {
	return &Server{links: st, linkPayRateLimiter: unlimitedLimiter{}}
}

// buildLinkServerWithCard monta um Server com link store + efiCobr apontando para srv de teste.
func buildLinkServerWithCard(st *fakeLinkStore, efiBase string) *Server {
	cobr := newTestCobr(efiBase)
	return &Server{links: st, efiCobr: cobr, linkPayRateLimiter: unlimitedLimiter{}}
}

// linkFixture cria um link ativo no fake store e devolve o token.
func linkFixture(t *testing.T, st *fakeLinkStore, amount *int64, methods []string) string {
	t.Helper()
	if methods == nil {
		methods = []string{"pix"}
	}
	l := &PaymentLink{
		PublicToken: newPublicToken(),
		AmountCents: amount,
		ProductIDs:  []int{},
		Methods:     methods,
		Coupons:     []string{},
	}
	if err := st.CreatePaymentLink(context.Background(), l); err != nil {
		t.Fatalf("linkFixture: %v", err)
	}
	return l.PublicToken
}

// ── Testes admin: POST /payment-links ─────────────────────────────────────────

func TestHandleCreatePaymentLink_Happy(t *testing.T) {
	st := newFakeLinkStore()
	s := buildLinkServer(st)

	body := `{"amountCents":5000,"methods":["pix","card"],"finishUrl":"https://example.com/ok"}`
	req := httptest.NewRequest("POST", "/payment-links", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleCreatePaymentLink(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("esperado 201, veio %d: %s", w.Code, w.Body.String())
	}
	var out PaymentLink
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ID == 0 {
		t.Fatal("ID deveria ser preenchido")
	}
	if out.Status != "active" {
		t.Fatalf("status esperado active, veio %q", out.Status)
	}
	if out.PublicToken == "" {
		t.Fatal("publicToken deveria ser preenchido")
	}
	if out.AmountCents == nil || *out.AmountCents != 5000 {
		t.Fatalf("amountCents esperado 5000, veio %v", out.AmountCents)
	}
}

func TestHandleCreatePaymentLink_InvalidMethod(t *testing.T) {
	st := newFakeLinkStore()
	s := buildLinkServer(st)

	body := `{"amountCents":1000,"methods":["bitcoin"]}`
	req := httptest.NewRequest("POST", "/payment-links", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCreatePaymentLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para método inválido, veio %d", w.Code)
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "invalid_body" {
		t.Fatalf("code esperado invalid_body, veio %q", out["code"])
	}
}

func TestHandleCreatePaymentLink_InvalidAmount(t *testing.T) {
	st := newFakeLinkStore()
	s := buildLinkServer(st)

	body := `{"amountCents":0,"methods":["pix"]}`
	req := httptest.NewRequest("POST", "/payment-links", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCreatePaymentLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para amount=0, veio %d", w.Code)
	}
}

func TestHandleCreatePaymentLink_FreeValue(t *testing.T) {
	// Sem amountCents: link de valor livre
	st := newFakeLinkStore()
	s := buildLinkServer(st)

	body := `{"methods":["pix","boleto"]}`
	req := httptest.NewRequest("POST", "/payment-links", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCreatePaymentLink(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("esperado 201 para link de valor livre, veio %d: %s", w.Code, w.Body.String())
	}
	var out PaymentLink
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.AmountCents != nil {
		t.Fatalf("amountCents deveria ser nil para link de valor livre, veio %v", *out.AmountCents)
	}
}

// ── Testes admin: GET /payment-links e GET /payment-links/{id} ────────────────

func TestHandleListPaymentLinks_Empty(t *testing.T) {
	st := newFakeLinkStore()
	s := buildLinkServer(st)

	req := httptest.NewRequest("GET", "/payment-links", nil)
	w := httptest.NewRecorder()
	s.handleListPaymentLinks(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d", w.Code)
	}
	var out []PaymentLink
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out) != 0 {
		t.Fatalf("esperado lista vazia, veio %d itens", len(out))
	}
}

func TestHandleListPaymentLinks_WithItems(t *testing.T) {
	st := newFakeLinkStore()
	linkFixture(t, st, nil, []string{"pix"})
	linkFixture(t, st, nil, []string{"card"})
	s := buildLinkServer(st)

	req := httptest.NewRequest("GET", "/payment-links", nil)
	w := httptest.NewRecorder()
	s.handleListPaymentLinks(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d", w.Code)
	}
	var out []PaymentLink
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out) != 2 {
		t.Fatalf("esperado 2 links, veio %d", len(out))
	}
}

func TestHandleGetPaymentLink_NotFound(t *testing.T) {
	st := newFakeLinkStore()
	s := buildLinkServer(st)

	req := httptest.NewRequest("GET", "/payment-links/999", nil)
	req.SetPathValue("id", "999")
	w := httptest.NewRecorder()
	s.handleGetPaymentLink(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("esperado 404, veio %d", w.Code)
	}
}

func TestHandleGetPaymentLink_Happy(t *testing.T) {
	st := newFakeLinkStore()
	token := linkFixture(t, st, nil, []string{"pix", "card"})
	link := st.links[token]
	s := buildLinkServer(st)

	idStr := strings.Builder{}
	idStr.WriteString("1")
	req := httptest.NewRequest("GET", "/payment-links/1", nil)
	req.SetPathValue("id", "1")
	_ = link
	w := httptest.NewRecorder()
	s.handleGetPaymentLink(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var out PaymentLink
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.ID != 1 {
		t.Fatalf("ID esperado 1, veio %d", out.ID)
	}
}

func TestHandleGetPaymentLink_InvalidID(t *testing.T) {
	st := newFakeLinkStore()
	s := buildLinkServer(st)

	req := httptest.NewRequest("GET", "/payment-links/abc", nil)
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	s.handleGetPaymentLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para ID inválido, veio %d", w.Code)
	}
}

// ── Testes admin: PATCH /payment-links/{id} ───────────────────────────────────

func TestHandlePatchPaymentLink_Inactivate(t *testing.T) {
	st := newFakeLinkStore()
	token := linkFixture(t, st, nil, []string{"pix"})
	s := buildLinkServer(st)

	req := httptest.NewRequest("PATCH", "/payment-links/1", strings.NewReader(`{"status":"inactive"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	s.handlePatchPaymentLink(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	// Confirma que o status mudou no store.
	if st.links[token].Status != "inactive" {
		t.Fatalf("status esperado inactive no store, veio %q", st.links[token].Status)
	}
}

func TestHandlePatchPaymentLink_InvalidStatus(t *testing.T) {
	st := newFakeLinkStore()
	linkFixture(t, st, nil, []string{"pix"})
	s := buildLinkServer(st)

	req := httptest.NewRequest("PATCH", "/payment-links/1", strings.NewReader(`{"status":"deleted"}`))
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	s.handlePatchPaymentLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para status inválido, veio %d", w.Code)
	}
}

func TestHandlePatchPaymentLink_NotFound(t *testing.T) {
	st := newFakeLinkStore()
	s := buildLinkServer(st)

	req := httptest.NewRequest("PATCH", "/payment-links/999", strings.NewReader(`{"status":"inactive"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "999")
	w := httptest.NewRecorder()
	s.handlePatchPaymentLink(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("esperado 404, veio %d", w.Code)
	}
}

// ── Testes públicos: GET /link/{token} ────────────────────────────────────────

func TestHandleGetLinkByToken_Active(t *testing.T) {
	st := newFakeLinkStore()
	amount := int64(9900)
	token := linkFixture(t, st, &amount, []string{"pix", "card"})
	s := buildLinkServer(st)

	req := httptest.NewRequest("GET", "/link/"+token, nil)
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handleGetLinkByToken(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var out PaymentLink
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.PublicToken != token {
		t.Fatalf("publicToken esperado %q, veio %q", token, out.PublicToken)
	}
	if out.AmountCents == nil || *out.AmountCents != 9900 {
		t.Fatalf("amountCents esperado 9900, veio %v", out.AmountCents)
	}
	if len(out.Methods) != 2 {
		t.Fatalf("esperado 2 métodos, veio %d", len(out.Methods))
	}
}

func TestHandleGetLinkByToken_Inactive(t *testing.T) {
	st := newFakeLinkStore()
	token := linkFixture(t, st, nil, []string{"pix"})
	_ = st.SetPaymentLinkStatus(context.Background(), 1, "inactive")
	s := buildLinkServer(st)

	req := httptest.NewRequest("GET", "/link/"+token, nil)
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handleGetLinkByToken(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("esperado 410 para link inativo, veio %d", w.Code)
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "link_inactive" {
		t.Fatalf("code esperado link_inactive, veio %q", out["code"])
	}
}

func TestHandleGetLinkByToken_NotFound(t *testing.T) {
	st := newFakeLinkStore()
	s := buildLinkServer(st)

	req := httptest.NewRequest("GET", "/link/token-inexistente", nil)
	req.SetPathValue("token", "token-inexistente")
	w := httptest.NewRecorder()
	s.handleGetLinkByToken(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("esperado 404, veio %d", w.Code)
	}
}

// ── Testes de pagar via link: POST /link/{token}/pay ──────────────────────────

func TestHandlePayViaLink_CardHappy(t *testing.T) {
	var chargeOneStepcalled atomic.Bool

	srv := cobrMux(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/v1/charge/one-step", func(w http.ResponseWriter, r *http.Request) {
			chargeOneStepcalled.Store(true)
			json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{"charge_id": 77001, "status": "waiting", "message": ""},
			})
		})
	})
	defer srv.Close()

	st := newFakeLinkStore()
	amount := int64(5000)
	token := linkFixture(t, st, &amount, []string{"pix", "card"})
	s := buildLinkServerWithCard(st, srv.URL)

	body := `{
		"method":"card",
		"paymentToken":"pt_test_xyz",
		"installments":1,
		"customer":{"name":"Ana","taxId":"12345678901","email":"ana@example.com"},
		"billingAddress":{"street":"Rua A","number":"1","neighborhood":"Centro","zipCode":"01310100","city":"SP","state":"SP"}
	}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("esperado 201, veio %d: %s", w.Code, w.Body.String())
	}
	if !chargeOneStepcalled.Load() {
		t.Fatal("endpoint Efí não foi chamado")
	}
	var out Charge
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Method != "card" {
		t.Fatalf("method esperado card, veio %q", out.Method)
	}
	if out.AmountCents != 5000 {
		t.Fatalf("amountCents esperado 5000, veio %d", out.AmountCents)
	}
	if out.LinkID == nil || *out.LinkID != 1 {
		t.Fatalf("linkId esperado 1, veio %v", out.LinkID)
	}
	// Confirma que a charge foi registrada no store.
	if len(st.charges) != 1 {
		t.Fatalf("esperado 1 charge no store, veio %d", len(st.charges))
	}
}

func TestHandlePayViaLink_MethodNotAllowed(t *testing.T) {
	st := newFakeLinkStore()
	token := linkFixture(t, st, nil, []string{"pix"}) // só pix
	s := buildLinkServer(st)

	body := `{"method":"card","amountCents":1000,"customer":{"name":"X","taxId":"1","email":"x@x.com"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para método não permitido, veio %d", w.Code)
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "method_not_allowed" {
		t.Fatalf("code esperado method_not_allowed, veio %q", out["code"])
	}
}

func TestHandlePayViaLink_FreeValueRequired(t *testing.T) {
	// Link de valor livre (AmountCents=nil) e o body não fornece amountCents.
	st := newFakeLinkStore()
	token := linkFixture(t, st, nil, []string{"pix"})
	s := buildLinkServer(st)

	body := `{"method":"pix","customer":{"name":"X","taxId":"1","email":"x@x.com"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 sem amountCents em link de valor livre, veio %d", w.Code)
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "invalid_body" {
		t.Fatalf("code esperado invalid_body, veio %q", out["code"])
	}
}

func TestHandlePayViaLink_InactiveLink(t *testing.T) {
	st := newFakeLinkStore()
	token := linkFixture(t, st, nil, []string{"pix"})
	_ = st.SetPaymentLinkStatus(context.Background(), 1, "inactive")
	s := buildLinkServer(st)

	body := `{"method":"pix","amountCents":1000,"customer":{"name":"X","taxId":"1","email":"x@x.com"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("esperado 410 para link inativo, veio %d", w.Code)
	}
}

func TestHandlePayViaLink_CardMissingPaymentToken(t *testing.T) {
	srv := cobrMux(t, nil)
	defer srv.Close()

	st := newFakeLinkStore()
	amount := int64(1000)
	token := linkFixture(t, st, &amount, []string{"card"})
	s := buildLinkServerWithCard(st, srv.URL)

	body := `{"method":"card","customer":{"name":"X","taxId":"1","email":"x@x.com"},"billingAddress":{}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 sem paymentToken, veio %d", w.Code)
	}
}

func TestHandlePayViaLink_BoletoMissingEfiCobr(t *testing.T) {
	// Sem efiCobr configurado → 503
	st := newFakeLinkStore()
	amount := int64(2000)
	token := linkFixture(t, st, &amount, []string{"boleto"})
	s := buildLinkServer(st) // sem efiCobr

	body := `{"method":"boleto","customer":{"name":"X","taxId":"12345678901","email":"x@x.com"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("esperado 503 sem efiCobr, veio %d", w.Code)
	}
}

func TestHandlePayViaLink_PixMissingProvider(t *testing.T) {
	// Sem provider PIX → 503
	st := newFakeLinkStore()
	amount := int64(3000)
	token := linkFixture(t, st, &amount, []string{"pix"})
	s := buildLinkServer(st) // sem provider

	body := `{"method":"pix","customer":{"name":"X","taxId":"12345678901","email":"x@x.com"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("esperado 503 sem provider PIX, veio %d", w.Code)
	}
}

func TestHandlePayViaLink_TokenNotFound(t *testing.T) {
	st := newFakeLinkStore()
	s := buildLinkServer(st)

	body := `{"method":"pix","amountCents":1000,"customer":{"name":"X","taxId":"1","email":"x@x.com"}}`
	req := httptest.NewRequest("POST", "/link/token-inexistente/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", "token-inexistente")
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("esperado 404, veio %d", w.Code)
	}
}

// ── Testes das correções de revisão ──────────────────────────────────────────

// [Fix 2] handlePayViaLink rejeita corpo com PAN/CVV (guard PCI para rota pública).
func TestHandlePayViaLink_RejectPAN(t *testing.T) {
	st := newFakeLinkStore()
	amount := int64(5000)
	token := linkFixture(t, st, &amount, []string{"pix"})
	s := buildLinkServer(st)

	// CVV aninhado — deve ser rejeitado com 400 sensitive_fields.
	body := `{"method":"pix","amountCents":5000,"customer":{"name":"X","taxId":"1"},"card":{"cvv":"123"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("[Fix2] esperado 400 para CVV no body, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "sensitive_fields" {
		t.Fatalf("[Fix2] code esperado sensitive_fields, veio %q", out["code"])
	}
}

// [Fix 3] Rate-limit por IP: após N requests, deve retornar 429.
func TestHandlePayViaLink_RateLimitIP(t *testing.T) {
	st := newFakeLinkStore()
	amount := int64(5000)
	token := linkFixture(t, st, &amount, []string{"pix"})
	// Usa o rate-limiter global real — primeiro Allow() passa, segundo bloqueia com burst=0.
	s := &Server{links: st, linkPayRateLimiter: blockedLimiter{}}

	body := `{"method":"pix","amountCents":5000,"customer":{"name":"X","taxId":"12345678901"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("[Fix3] esperado 429 rate_limited, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "rate_limited" {
		t.Fatalf("[Fix3] code esperado rate_limited, veio %q", out["code"])
	}
}

// [Fix 5] PIX via link requer customer.name e customer.taxId.
func TestHandlePayViaLink_PixMissingCustomer(t *testing.T) {
	st := newFakeLinkStore()
	amount := int64(5000)
	token := linkFixture(t, st, &amount, []string{"pix"})
	s := buildLinkServer(st)

	// Body sem customer.name.
	body := `{"method":"pix","customer":{"taxId":"12345678901"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("[Fix5] esperado 400 sem customer.name, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "invalid_body" {
		t.Fatalf("[Fix5] code esperado invalid_body, veio %q", out["code"])
	}
}

// [Fix 7] Teto de valor livre: amountCents > LinkMaxCents → 400 amount_too_large.
func TestHandlePayViaLink_AmountTooLarge(t *testing.T) {
	st := newFakeLinkStore()
	// Link de valor livre (AmountCents=nil).
	token := linkFixture(t, st, nil, []string{"pix"})
	s := &Server{
		links:              st,
		linkPayRateLimiter: unlimitedLimiter{},
		cfg:                Config{LinkMaxCents: 100_000}, // teto = R$ 1.000,00
	}

	body := `{"method":"pix","amountCents":200000,"customer":{"name":"X","taxId":"12345678901"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("[Fix7] esperado 400 amount_too_large, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "amount_too_large" {
		t.Fatalf("[Fix7] code esperado amount_too_large, veio %q", out["code"])
	}
}

// [Fix 7] Valor dentro do teto passa normalmente (sem provider, espera-se 503 pix_unavailable, não 400).
func TestHandlePayViaLink_AmountWithinLimit(t *testing.T) {
	st := newFakeLinkStore()
	token := linkFixture(t, st, nil, []string{"pix"})
	s := &Server{
		links:              st,
		linkPayRateLimiter: unlimitedLimiter{},
		cfg:                Config{LinkMaxCents: 100_000},
		// provider=nil → 503 pix_unavailable, mas não 400 amount_too_large
	}

	body := `{"method":"pix","amountCents":99999,"customer":{"name":"Ana","taxId":"12345678901"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	// Sem provider PIX → 503, mas NÃO 400 amount_too_large.
	if w.Code == http.StatusBadRequest {
		var out map[string]string
		json.Unmarshal(w.Body.Bytes(), &out)
		if out["code"] == "amount_too_large" {
			t.Fatal("[Fix7] valor dentro do teto não deveria retornar amount_too_large")
		}
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("[Fix7] esperado 503 (pix_unavailable), veio %d: %s", w.Code, w.Body.String())
	}
}
