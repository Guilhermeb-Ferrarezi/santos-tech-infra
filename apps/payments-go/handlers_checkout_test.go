package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// errFakeNotFound é o sentinel de "não encontrado" no fakeCheckoutStore.
var errFakeNotFound = errors.New("fake: not found")

// ── fakeCheckoutStore ────────────────────────────────────────────────────────

// fakeCheckoutStore implementa checkoutStore em memória, sem Postgres.
type fakeCheckoutStore struct {
	products       map[int64]*Product
	customers      map[int64]*Customer
	nextCustID     int64
	insertItemsErr error
}

func newFakeCheckoutStore(products ...*Product) *fakeCheckoutStore {
	f := &fakeCheckoutStore{
		products:  make(map[int64]*Product),
		customers: make(map[int64]*Customer),
	}
	for _, p := range products {
		f.products[p.ID] = p
	}
	return f
}

func (f *fakeCheckoutStore) GetProductByID(_ context.Context, id int64) (*Product, error) {
	p, ok := f.products[id]
	if !ok {
		return nil, errFakeNotFound
	}
	cp := *p
	return &cp, nil
}

func (f *fakeCheckoutStore) UpsertCustomer(_ context.Context, userID int64, _, _, _, _ string) (*Customer, error) {
	f.nextCustID++
	c := &Customer{ID: f.nextCustID, UserID: userID}
	f.customers[f.nextCustID] = c
	return c, nil
}

func (f *fakeCheckoutStore) InsertChargeItems(_ context.Context, _ int64, _ []ChargeItem) error {
	return f.insertItemsErr
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// checkoutReq monta um Server e executa handleCheckout com uid injetado.
// Para caminhos que param ANTES de createAndPersistCharge (ex.: erro de cupom),
// não precisamos de provider nem store real.
func checkoutReqWith(s *Server, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/me/cart/checkout", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), userIDKey, int64(1)))
	s.handleCheckout(w, req)
	return w
}

// newTestCartWithItems cria um CartStore em miniredis com itens pré-carregados.
func newTestCartWithItems(t *testing.T, userID int64, items ...CartItem) *CartStore {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cs := &CartStore{rdb: rdb}
	ctx := context.Background()
	for _, it := range items {
		for i := 0; i < it.Quantity; i++ {
			if err := cs.Add(ctx, userID, it.ProductID); err != nil {
				t.Fatalf("cart.Add: %v", err)
			}
		}
	}
	return cs
}

// ── Testes de validação do body (sem store) ───────────────────────────────────

func TestHandleCheckout_InvalidBody(t *testing.T) {
	s := &Server{}
	cases := []struct {
		name string
		body string
	}{
		{"json inválido", `{`},
		{"sem nome", `{"taxId":"39053344705","phone":"11999998888","name":"","email":"a@b.com"}`},
		{"email inválido", `{"taxId":"39053344705","phone":"11999998888","name":"Ana Silva","email":"nao-email"}`},
		{"cpf inválido", `{"taxId":"111","phone":"11999998888","name":"Ana Silva","email":"a@b.com"}`},
		{"telefone inválido", `{"taxId":"39053344705","phone":"123","name":"Ana Silva","email":"a@b.com"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := checkoutReqWith(s, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("esperado 400, veio %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

// ── Testes de cupom no checkout do carrinho ───────────────────────────────────

const validCheckoutBody = `{
	"taxId":"39053344705",
	"phone":"11999998888",
	"name":"Ana Silva",
	"email":"ana@exemplo.com",
	"coupon":"%s"
}`

// buildCheckoutServerWithCoupon monta Server com produto no carrinho + cupom.
// NÃO inclui provider: testes que chegam ao createAndPersistCharge falharão,
// mas para caminhos de erro de cupom (retornam antes) é suficiente.
func buildCheckoutServerWithCoupon(t *testing.T, cSt *fakeCouponStore) *Server {
	t.Helper()
	prod := &Product{ID: 1, Name: "Produto Teste", PriceCents: 10000, Recurring: false}
	chSt := newFakeCheckoutStore(prod)
	cart := newTestCartWithItems(t, 1, CartItem{ProductID: 1, Quantity: 1})
	return &Server{
		checkoutSt: chSt,
		coupons:    cSt,
		cart:       cart,
	}
}

func TestHandleCheckout_CouponInvalid_NotFound(t *testing.T) {
	// Cupom inexistente → 400 invalid_coupon.
	cSt := newFakeCouponStore()
	s := buildCheckoutServerWithCoupon(t, cSt)

	body := strings.NewReplacer("%s", "NAOEXI").Replace(validCheckoutBody)
	w := checkoutReqWith(s, body)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para cupom inexistente, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "invalid_coupon" {
		t.Fatalf("code esperado invalid_coupon, veio %q", out["code"])
	}
}

func TestHandleCheckout_CouponInvalid_Inactive(t *testing.T) {
	// Cupom inativo → 400 invalid_coupon.
	cSt := newFakeCouponStore()
	c := couponFixture(t, cSt, "INATIVO", "percent", 10, -1)
	_ = cSt.SetCouponActive(context.Background(), c.ID, false)

	s := buildCheckoutServerWithCoupon(t, cSt)
	body := strings.NewReplacer("%s", "INATIVO").Replace(validCheckoutBody)
	w := checkoutReqWith(s, body)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para cupom inativo, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "invalid_coupon" {
		t.Fatalf("code esperado invalid_coupon, veio %q", out["code"])
	}
}

func TestHandleCheckout_CouponInvalid_Exhausted(t *testing.T) {
	// Cupom esgotado → 400 invalid_coupon.
	cSt := newFakeCouponStore()
	c := couponFixture(t, cSt, "ESGOT", "percent", 10, 1)
	_ = cSt.IncrementCouponUse(context.Background(), c.ID)

	s := buildCheckoutServerWithCoupon(t, cSt)
	body := strings.NewReplacer("%s", "ESGOT").Replace(validCheckoutBody)
	w := checkoutReqWith(s, body)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para cupom esgotado, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "invalid_coupon" {
		t.Fatalf("code esperado invalid_coupon, veio %q", out["code"])
	}
}

func TestHandleCheckout_EmptyCartWithCoupon(t *testing.T) {
	// Carrinho vazio → 400 empty_cart, mesmo com cupom informado.
	// Garante que a validação de cupom não é atingida com carrinho inválido.
	cSt := newFakeCouponStore()
	couponFixture(t, cSt, "CUPOM10", "percent", 10, -1)

	prod := &Product{ID: 1, Name: "Produto", PriceCents: 5000}
	chSt := newFakeCheckoutStore(prod)
	// Carrinho vazio: não adiciona nada
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	emptyCart := &CartStore{rdb: redis.NewClient(&redis.Options{Addr: mr.Addr()})}

	s := &Server{
		checkoutSt: chSt,
		coupons:    cSt,
		cart:       emptyCart,
	}

	body := strings.NewReplacer("%s", "CUPOM10").Replace(validCheckoutBody)
	w := checkoutReqWith(s, body)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 (empty_cart), veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "empty_cart" {
		t.Fatalf("code esperado empty_cart, veio %q", out["code"])
	}
}
