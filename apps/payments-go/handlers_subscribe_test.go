package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeSubStore implementa subscribeStore para os testes do checkout recorrente (sem Postgres).
type fakeSubStore struct {
	product        *Product
	productErr     error
	customer       *Customer
	upsertErr      error
	insertErr      error
	insertedCharge *Charge
}

func (f *fakeSubStore) GetProductByID(_ context.Context, _ int64) (*Product, error) {
	return f.product, f.productErr
}
func (f *fakeSubStore) UpsertCustomer(_ context.Context, _ int64, _, _, _, _ string) (*Customer, error) {
	if f.upsertErr != nil {
		return nil, f.upsertErr
	}
	if f.customer != nil {
		return f.customer, nil
	}
	return &Customer{ID: 42}, nil
}
func (f *fakeSubStore) InsertRecurrenceCharge(_ context.Context, c *Charge) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	c.ID = 100
	c.Status = "pending"
	f.insertedCharge = c
	return nil
}

func intp(v int) *int { return &v }

// subscribeReq monta um *Server com a sessão (uid) injetada via authGuard simulado.
func subscribeReq(s *Server, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/me/subscribe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), userIDKey, int64(7)))
	s.handleSubscribe(w, req)
	return w
}

// Validações de body rodam antes de tocar store/Efí: cobrimos os 400 sem Postgres.
func TestHandleSubscribeValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"json invalido", `{`},
		{"sem productId", `{"taxId":"39053344705","phone":"11999998888","name":"Fulano","email":"a@b.com"}`},
		{"sem nome", `{"productId":1,"taxId":"39053344705","phone":"11999998888","name":"  ","email":"a@b.com"}`},
		{"email invalido", `{"productId":1,"taxId":"39053344705","phone":"11999998888","name":"Fulano","email":"naoemail"}`},
		{"cpf invalido", `{"productId":1,"taxId":"111","phone":"11999998888","name":"Fulano","email":"a@b.com"}`},
		{"telefone invalido", `{"productId":1,"taxId":"39053344705","phone":"123","name":"Fulano","email":"a@b.com"}`},
	}
	s := &Server{efi: fakeEfiOps{}, recs: &fakeRecStore{}, subs: &fakeSubStore{}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := subscribeReq(s, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("esperado 400, veio %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

// Produto não recorrente → 409 not_recurring (não vira assinatura).
func TestHandleSubscribeProductNotRecurring(t *testing.T) {
	s := &Server{
		efi:  fakeEfiOps{},
		recs: &fakeRecStore{},
		subs: &fakeSubStore{product: &Product{ID: 1, Name: "Curso", PriceCents: 5000, Recurring: false}},
	}
	body := `{"productId":1,"taxId":"39053344705","phone":"11999998888","name":"Fulano de Tal","email":"a@b.com"}`
	w := subscribeReq(s, body)
	if w.Code != http.StatusConflict {
		t.Fatalf("esperado 409, veio %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Code != "not_recurring" {
		t.Fatalf("code esperado not_recurring, veio %q", out.Code)
	}
}

// Produto inexistente → 404.
func TestHandleSubscribeProductNotFound(t *testing.T) {
	s := &Server{
		efi:  fakeEfiOps{},
		recs: &fakeRecStore{},
		subs: &fakeSubStore{productErr: errors.New("no rows")},
	}
	body := `{"productId":99,"taxId":"39053344705","phone":"11999998888","name":"Fulano de Tal","email":"a@b.com"}`
	w := subscribeReq(s, body)
	if w.Code != http.StatusNotFound {
		t.Fatalf("esperado 404, veio %d: %s", w.Code, w.Body.String())
	}
}

// Produto recorrente mal configurado (periodicity/due_day) → 409 invalid_product.
func TestHandleSubscribeProductMisconfigured(t *testing.T) {
	s := &Server{
		efi:  fakeEfiOps{},
		recs: &fakeRecStore{},
		subs: &fakeSubStore{product: &Product{ID: 1, Name: "Assinatura", PriceCents: 5000, Recurring: true, Periodicity: "", DueDay: nil}},
	}
	body := `{"productId":1,"taxId":"39053344705","phone":"11999998888","name":"Fulano de Tal","email":"a@b.com"}`
	w := subscribeReq(s, body)
	if w.Code != http.StatusConflict {
		t.Fatalf("esperado 409, veio %d: %s", w.Code, w.Body.String())
	}
}

// Caminho feliz: produto recorrente → cria recorrência (journey=3) + 1ª cobr e devolve
// token/brCode/qrCode/amountCents.
func TestHandleSubscribeHappy(t *testing.T) {
	rs := &fakeRecStore{}
	ss := &fakeSubStore{
		product:  &Product{ID: 3, Name: "Plano Pro", PriceCents: 9900, Recurring: true, Periodicity: "MENSAL", DueDay: intp(10)},
		customer: &Customer{ID: 55},
	}
	s := &Server{efi: fakeEfiOps{}, recs: rs, subs: ss}
	body := `{"productId":3,"taxId":"39053344705","phone":"11999998888","name":"Fulano de Tal","email":"a@b.com","save":true}`
	w := subscribeReq(s, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("esperado 201, veio %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Token       string `json:"token"`
		BRCode      string `json:"brCode"`
		QRCode      string `json:"qrCode"`
		AmountCents int64  `json:"amountCents"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Token == "" || out.BRCode != "00020101br-code-j3" || out.QRCode == "" || out.AmountCents != 9900 {
		t.Fatalf("resposta inesperada: %+v", out)
	}
	// Recorrência persistida com journey=3, periodicity e due_day do produto.
	if rs.created == nil || rs.created.Journey != 3 || rs.created.Periodicity != "MENSAL" ||
		rs.created.DueDay == nil || *rs.created.DueDay != 10 || rs.created.ProductID == nil || *rs.created.ProductID != 3 {
		t.Fatalf("recorrência persistida inesperada: %+v", rs.created)
	}
	if !rs.authCalled {
		t.Fatal("esperava UpdateRecurrenceAuth após a Efí")
	}
	// 1ª cobr: kind recorrente (via InsertRecurrenceCharge), com public_token e correlation_id = txid.
	if ss.insertedCharge == nil || ss.insertedCharge.PublicToken == "" ||
		ss.insertedCharge.RecurrenceID == nil || ss.insertedCharge.CorrelationID == "" {
		t.Fatalf("1ª cobrança inesperada: %+v", ss.insertedCharge)
	}
	if ss.insertedCharge.CustomerID == nil || *ss.insertedCharge.CustomerID != 55 {
		t.Fatalf("cobrança deveria ligar ao customer 55: %+v", ss.insertedCharge)
	}
}

// Efí falha ao criar a Jornada 3 → status local vira 'rejected' e responde 422.
func TestHandleSubscribeProviderError(t *testing.T) {
	rs := &fakeRecStore{}
	ss := &fakeSubStore{
		product: &Product{ID: 3, Name: "Plano Pro", PriceCents: 9900, Recurring: true, Periodicity: "MENSAL", DueDay: intp(10)},
	}
	s := &Server{efi: failingJ3EfiOps{}, recs: rs, subs: ss}
	body := `{"productId":3,"taxId":"39053344705","phone":"11999998888","name":"Fulano de Tal","email":"a@b.com"}`
	w := subscribeReq(s, body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("esperado 422, veio %d: %s", w.Code, w.Body.String())
	}
	if len(rs.statusSet) != 1 || rs.statusSet[0] != "rejected" {
		t.Fatalf("esperava SetRecurrenceStatus('rejected'), veio %v", rs.statusSet)
	}
}

// failingJ3EfiOps falha só na Jornada 3 (reusa o resto do fakeEfiOps).
type failingJ3EfiOps struct{ fakeEfiOps }

func (failingJ3EfiOps) CreateRecurrenceJornada3(_ context.Context, _ RecurrenceJornada3Request) (RecurrenceResult, ChargeResult, error) {
	return RecurrenceResult{}, ChargeResult{}, errors.New("efi indisponível")
}
