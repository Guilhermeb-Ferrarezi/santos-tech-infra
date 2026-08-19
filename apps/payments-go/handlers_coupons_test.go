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
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/time/rate"
)

// ── Fake store de cupons ──────────────────────────────────────────────────────

// fakeCouponStore é um stub em memória de couponStore para testes sem DB.
type fakeCouponStore struct {
	// mu protege os mapas: RedeemCoupon precisa ser atômico de verdade para que o
	// teste de concorrência exercite o mesmo contrato do UPDATE ... RETURNING.
	mu     sync.Mutex
	byID   map[int64]*Coupon
	byCode map[string]*Coupon // indexado por UPPER(code)
	nextID atomic.Int64
	failOn string // se não vazio, métodos retornam erro
	// Contador de resgates efetivados (verificação em testes de pagamento).
	incrementCalls atomic.Int64
	// Contador de devoluções (pagamento que não vingou).
	releaseCalls atomic.Int64
}

func newFakeCouponStore() *fakeCouponStore {
	return &fakeCouponStore{
		byID:   make(map[int64]*Coupon),
		byCode: make(map[string]*Coupon),
	}
}

func (f *fakeCouponStore) CreateCoupon(_ context.Context, c Coupon) (Coupon, error) {
	if f.failOn == "create" {
		return Coupon{}, errors.New("db error fake")
	}
	upper := strings.ToUpper(c.Code)
	if _, exists := f.byCode[upper]; exists {
		// Simula violação de UNIQUE (code 23505); couponUniqueViolation não irá
		// detectar este erro fake — o handler testa via erro específico.
		// Para simplificar, retornamos um sentinel que o handler mapeia para 409.
		return Coupon{}, errFakeDuplicate
	}
	id := f.nextID.Add(1)
	c.ID = id
	c.UsedCount = 0
	c.Active = true
	c.CreatedAt = time.Now()
	cp := c
	f.byID[id] = &cp
	f.byCode[upper] = &cp
	return cp, nil
}

func (f *fakeCouponStore) ListCoupons(_ context.Context, _ listPage) ([]Coupon, error) {
	if f.failOn == "list" {
		return nil, errors.New("db error fake")
	}
	out := make([]Coupon, 0, len(f.byID))
	for _, c := range f.byID {
		out = append(out, *c)
	}
	return out, nil
}

func (f *fakeCouponStore) GetCouponByCode(_ context.Context, code string) (Coupon, error) {
	if f.failOn == "get" {
		return Coupon{}, errors.New("db error fake")
	}
	upper := strings.ToUpper(code)
	c, ok := f.byCode[upper]
	if !ok {
		return Coupon{}, pgx.ErrNoRows
	}
	return *c, nil
}

func (f *fakeCouponStore) SetCouponActive(_ context.Context, id int64, active bool) error {
	if f.failOn == "patch" {
		return errors.New("db error fake")
	}
	c, ok := f.byID[id]
	if !ok {
		return pgx.ErrNoRows
	}
	c.Active = active
	return nil
}

// IncrementCouponUse não faz parte de couponStore — é só um utilitário dos testes
// para esgotar um cupom antes do cenário.
func (f *fakeCouponStore) IncrementCouponUse(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.incrementCalls.Add(1)
	c, ok := f.byID[id]
	if !ok {
		return pgx.ErrNoRows
	}
	c.UsedCount++
	return nil
}

// RedeemCoupon reproduz o UPDATE ... WHERE active AND used_count < max_uses RETURNING:
// checagem de limite e incremento sob o mesmo lock, sem janela entre os dois.
func (f *fakeCouponStore) RedeemCoupon(_ context.Context, code string) (Coupon, error) {
	if f.failOn == "get" {
		return Coupon{}, errors.New("db error fake")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.byCode[strings.ToUpper(strings.TrimSpace(code))]
	if !ok || !c.Active {
		return Coupon{}, pgx.ErrNoRows
	}
	if c.MaxUses != -1 && c.UsedCount >= c.MaxUses {
		return Coupon{}, pgx.ErrNoRows
	}
	c.UsedCount++
	f.incrementCalls.Add(1)
	return *c, nil
}

func (f *fakeCouponStore) ReleaseCouponUse(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls.Add(1)
	c, ok := f.byID[id]
	if !ok {
		return pgx.ErrNoRows
	}
	if c.UsedCount > 0 {
		c.UsedCount--
	}
	return nil
}

// errFakeDuplicate é um sentinel de duplicidade usado pelo fake store.
// O handler usa couponUniqueViolation(err) para detectar o erro real de Postgres,
// mas no fake precisamos de uma checagem alternativa no stub.
// A solução: o fakeCouponStore retorna errFakeDuplicate, e
// o handleCreateCoupon recebe o erro — mas couponUniqueViolation retornará false
// neste caso (sem PgError). Para cobrir o 409 nos testes sem PgError real, o
// fake store é estendido com um flag "dupCode" para forçar o erro no 2º create.
var errFakeDuplicate = errors.New("fake: unique violation")

// ── Helpers ───────────────────────────────────────────────────────────────────

// buildCouponServer monta um Server com o fakeCouponStore injetado.
func buildCouponServer(st *fakeCouponStore) *Server {
	// Sem limitador nos testes de lógica: o rate-limit real de /coupons/apply é por
	// IP e process-level, e todos os requests de teste vêm do mesmo IP.
	return &Server{coupons: st, couponApplyRateLimiter: unlimitedLimiter{}}
}

// couponFixture cria um cupom no fake store e devolve o Coupon resultante.
func couponFixture(t *testing.T, st *fakeCouponStore, code, dtype string, value, maxUses int64) Coupon {
	t.Helper()
	c, err := st.CreateCoupon(context.Background(), Coupon{
		Code:          code,
		DiscountType:  dtype,
		DiscountValue: value,
		MaxUses:       maxUses,
	})
	if err != nil {
		t.Fatalf("couponFixture: %v", err)
	}
	return c
}

// ── POST /coupons ─────────────────────────────────────────────────────────────

func TestHandleCreateCoupon_Happy(t *testing.T) {
	st := newFakeCouponStore()
	s := buildCouponServer(st)

	body := `{"code":"DESCONTO10","discountType":"percent","discountValue":10}`
	req := httptest.NewRequest("POST", "/coupons", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleCreateCoupon(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("esperado 201, veio %d: %s", w.Code, w.Body.String())
	}
	var out Coupon
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ID == 0 {
		t.Fatal("ID deveria ser preenchido")
	}
	if out.Code != "DESCONTO10" {
		t.Fatalf("code esperado DESCONTO10, veio %q", out.Code)
	}
	if out.DiscountType != "percent" {
		t.Fatalf("discountType esperado percent, veio %q", out.DiscountType)
	}
	if out.DiscountValue != 10 {
		t.Fatalf("discountValue esperado 10, veio %d", out.DiscountValue)
	}
	if out.MaxUses != -1 {
		t.Fatalf("maxUses esperado -1 (ilimitado), veio %d", out.MaxUses)
	}
	if !out.Active {
		t.Fatal("active deveria ser true")
	}
}

func TestHandleCreateCoupon_Fixed(t *testing.T) {
	st := newFakeCouponStore()
	s := buildCouponServer(st)

	body := `{"code":"R50OFF","discountType":"fixed","discountValue":5000,"maxUses":100}`
	req := httptest.NewRequest("POST", "/coupons", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCreateCoupon(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("esperado 201, veio %d: %s", w.Code, w.Body.String())
	}
	var out Coupon
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.MaxUses != 100 {
		t.Fatalf("maxUses esperado 100, veio %d", out.MaxUses)
	}
}

func TestHandleCreateCoupon_EmptyCode(t *testing.T) {
	st := newFakeCouponStore()
	s := buildCouponServer(st)

	body := `{"code":"","discountType":"percent","discountValue":10}`
	req := httptest.NewRequest("POST", "/coupons", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCreateCoupon(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para code vazio, veio %d", w.Code)
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "invalid_body" {
		t.Fatalf("code esperado invalid_body, veio %q", out["code"])
	}
}

func TestHandleCreateCoupon_InvalidType(t *testing.T) {
	st := newFakeCouponStore()
	s := buildCouponServer(st)

	body := `{"code":"X","discountType":"absolute","discountValue":10}`
	req := httptest.NewRequest("POST", "/coupons", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCreateCoupon(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para tipo inválido, veio %d", w.Code)
	}
}

func TestHandleCreateCoupon_PercentOver100(t *testing.T) {
	st := newFakeCouponStore()
	s := buildCouponServer(st)

	body := `{"code":"X","discountType":"percent","discountValue":150}`
	req := httptest.NewRequest("POST", "/coupons", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCreateCoupon(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para percent>100, veio %d", w.Code)
	}
}

func TestHandleCreateCoupon_ZeroValue(t *testing.T) {
	st := newFakeCouponStore()
	s := buildCouponServer(st)

	body := `{"code":"X","discountType":"fixed","discountValue":0}`
	req := httptest.NewRequest("POST", "/coupons", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCreateCoupon(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para discountValue=0, veio %d", w.Code)
	}
}

func TestHandleCreateCoupon_Duplicate(t *testing.T) {
	// O fake store retorna errFakeDuplicate ao criar um código que já existe.
	// couponUniqueViolation retorna false porque não é PgError, mas o handler
	// testa a duplicidade via o fake store — para testar o 409, precisamos de um
	// fakeCouponStore que retorne um PgError ou um wrapper.
	// Solução pragmática: criar um fake store que retorne um erro contendo "23505"
	// ou usar um couponStore alternativo que força o 409.
	// Usamos um wrapper do fakeCouponStore que substitui CreateCoupon.
	st := newFakeCouponStore()
	s := &Server{
		coupons: &fakeDupCouponStore{inner: st},
	}

	body := `{"code":"PROMO","discountType":"percent","discountValue":5}`
	req := httptest.NewRequest("POST", "/coupons", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCreateCoupon(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("esperado 409 para cupom duplicado, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "coupon_exists" {
		t.Fatalf("code esperado coupon_exists, veio %q", out["code"])
	}
}

// ── GET /coupons ──────────────────────────────────────────────────────────────

func TestHandleListCoupons_Empty(t *testing.T) {
	st := newFakeCouponStore()
	s := buildCouponServer(st)

	req := httptest.NewRequest("GET", "/coupons", nil)
	w := httptest.NewRecorder()
	s.handleListCoupons(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d", w.Code)
	}
	var out []Coupon
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out) != 0 {
		t.Fatalf("esperado lista vazia, veio %d itens", len(out))
	}
}

func TestHandleListCoupons_WithItems(t *testing.T) {
	st := newFakeCouponStore()
	couponFixture(t, st, "A", "fixed", 1000, -1)
	couponFixture(t, st, "B", "percent", 20, 50)
	s := buildCouponServer(st)

	req := httptest.NewRequest("GET", "/coupons", nil)
	w := httptest.NewRecorder()
	s.handleListCoupons(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d", w.Code)
	}
	var out []Coupon
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out) != 2 {
		t.Fatalf("esperado 2 cupons, veio %d", len(out))
	}
}

// ── PATCH /coupons/{id} ───────────────────────────────────────────────────────

func TestHandlePatchCoupon_Deactivate(t *testing.T) {
	st := newFakeCouponStore()
	c := couponFixture(t, st, "ATIVO", "percent", 10, -1)
	s := buildCouponServer(st)

	body := `{"active":false}`
	req := httptest.NewRequest("PATCH", "/coupons/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	s.handlePatchCoupon(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	// Confirma que active mudou no store.
	stored := st.byID[c.ID]
	if stored.Active {
		t.Fatal("active deveria ser false após PATCH")
	}
}

func TestHandlePatchCoupon_NotFound(t *testing.T) {
	st := newFakeCouponStore()
	s := buildCouponServer(st)

	body := `{"active":false}`
	req := httptest.NewRequest("PATCH", "/coupons/999", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "999")
	w := httptest.NewRecorder()
	s.handlePatchCoupon(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("esperado 404, veio %d", w.Code)
	}
}

func TestHandlePatchCoupon_InvalidID(t *testing.T) {
	st := newFakeCouponStore()
	s := buildCouponServer(st)

	req := httptest.NewRequest("PATCH", "/coupons/abc", strings.NewReader(`{"active":false}`))
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	s.handlePatchCoupon(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400, veio %d", w.Code)
	}
}

// ── POST /coupons/apply ───────────────────────────────────────────────────────

func TestHandleApplyCoupon_ValidFixed(t *testing.T) {
	st := newFakeCouponStore()
	couponFixture(t, st, "FIXO50", "fixed", 5000, -1) // R$ 50 de desconto
	s := buildCouponServer(st)

	body := `{"code":"FIXO50","amountCents":20000}` // R$ 200 → desconto R$ 50 → final R$ 150
	req := httptest.NewRequest("POST", "/coupons/apply", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleApplyCoupon(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var out applyCouponResult
	json.Unmarshal(w.Body.Bytes(), &out)
	if !out.Valid {
		t.Fatalf("válido esperado true, veio false: %s", out.Reason)
	}
	if out.DiscountCents != 5000 {
		t.Fatalf("discountCents esperado 5000, veio %d", out.DiscountCents)
	}
	if out.FinalCents != 15000 {
		t.Fatalf("finalCents esperado 15000, veio %d", out.FinalCents)
	}
}

func TestHandleApplyCoupon_ValidPercent(t *testing.T) {
	st := newFakeCouponStore()
	couponFixture(t, st, "PCT10", "percent", 10, -1) // 10% de desconto
	s := buildCouponServer(st)

	body := `{"code":"PCT10","amountCents":10000}` // R$ 100 → desconto R$ 10 → final R$ 90
	req := httptest.NewRequest("POST", "/coupons/apply", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleApplyCoupon(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var out applyCouponResult
	json.Unmarshal(w.Body.Bytes(), &out)
	if !out.Valid {
		t.Fatalf("válido esperado true, veio false: %s", out.Reason)
	}
	if out.DiscountCents != 1000 {
		t.Fatalf("discountCents esperado 1000, veio %d", out.DiscountCents)
	}
	if out.FinalCents != 9000 {
		t.Fatalf("finalCents esperado 9000, veio %d", out.FinalCents)
	}
}

func TestHandleApplyCoupon_FixedExceedsAmount(t *testing.T) {
	// Cupom de R$ 500 em compra de R$ 100 → desconto máximo = R$ 100, final = R$ 0.
	st := newFakeCouponStore()
	couponFixture(t, st, "GRATIS", "fixed", 50000, -1)
	s := buildCouponServer(st)

	body := `{"code":"GRATIS","amountCents":10000}`
	req := httptest.NewRequest("POST", "/coupons/apply", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleApplyCoupon(w, req)

	var out applyCouponResult
	json.Unmarshal(w.Body.Bytes(), &out)
	if !out.Valid {
		t.Fatalf("válido esperado true, veio false")
	}
	if out.DiscountCents != 10000 {
		t.Fatalf("discountCents esperado 10000 (limitado ao amount), veio %d", out.DiscountCents)
	}
	if out.FinalCents != 0 {
		t.Fatalf("finalCents esperado 0, veio %d", out.FinalCents)
	}
}

func TestHandleApplyCoupon_NotFound(t *testing.T) {
	st := newFakeCouponStore()
	s := buildCouponServer(st)

	body := `{"code":"INEXISTENTE","amountCents":10000}`
	req := httptest.NewRequest("POST", "/coupons/apply", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleApplyCoupon(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200 (valid:false), veio %d", w.Code)
	}
	var out applyCouponResult
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Valid {
		t.Fatal("válido esperado false para cupom inexistente")
	}
	if out.Reason == "" {
		t.Fatal("reason deveria ser preenchido")
	}
}

func TestHandleApplyCoupon_Inactive(t *testing.T) {
	st := newFakeCouponStore()
	c := couponFixture(t, st, "INATIVO", "percent", 5, -1)
	_ = st.SetCouponActive(context.Background(), c.ID, false)
	s := buildCouponServer(st)

	body := `{"code":"INATIVO","amountCents":10000}`
	req := httptest.NewRequest("POST", "/coupons/apply", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleApplyCoupon(w, req)

	var out applyCouponResult
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Valid {
		t.Fatal("válido esperado false para cupom inativo")
	}
	// Razão GENÉRICA de propósito: distinguir "não existe" de "inativo" na rota
	// pública permitia enumerar cupons por dicionário.
	if out.Reason != couponInvalid.Reason {
		t.Fatalf("reason deveria ser genérica (%q), veio %q", couponInvalid.Reason, out.Reason)
	}
}

func TestHandleApplyCoupon_Exhausted(t *testing.T) {
	st := newFakeCouponStore()
	c := couponFixture(t, st, "ESGOTADO", "percent", 15, 2) // max_uses=2
	// Simula 2 usos já feitos.
	_ = st.IncrementCouponUse(context.Background(), c.ID)
	_ = st.IncrementCouponUse(context.Background(), c.ID)
	s := buildCouponServer(st)

	body := `{"code":"ESGOTADO","amountCents":10000}`
	req := httptest.NewRequest("POST", "/coupons/apply", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleApplyCoupon(w, req)

	var out applyCouponResult
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Valid {
		t.Fatal("válido esperado false para cupom esgotado")
	}
	if out.Reason != couponInvalid.Reason {
		t.Fatalf("reason deveria ser genérica (%q), veio %q", couponInvalid.Reason, out.Reason)
	}
}

// ── Teste de integração: pagamento via link com cupom ────────────────────────
// Usa o fakeLinkStore + fakeCouponStore + fakePIX provider.

// fakePixProvider implementa PaymentProvider para testes sem Efí real.
type fakePixProvider struct{}

func (f *fakePixProvider) CreateCharge(_ context.Context, req ChargeRequest) (ChargeResult, error) {
	return ChargeResult{
		CorrelationID:    req.CorrelationID,
		ProviderChargeID: "fake-pix-id",
		BRCode:           "fake-brcode",
		QRCode:           "",
	}, nil
}

func (f *fakePixProvider) GetCharge(_ context.Context, _ string) (ChargeResult, error) {
	return ChargeResult{}, nil
}

func (f *fakePixProvider) ParseWebhook(_ map[string][]string, _ []byte) ([]WebhookEvent, error) {
	return nil, nil
}

func TestHandlePayViaLink_WithCouponFixed(t *testing.T) {
	// Monta: link de R$ 100, cupom fixed de R$ 20 → charge de R$ 80.
	lSt := newFakeLinkStore()
	amount := int64(10000) // R$ 100
	token := linkFixture(t, lSt, &amount, []string{"pix"})

	cSt := newFakeCouponStore()
	coup := couponFixture(t, cSt, "MINUS20", "fixed", 2000, -1) // R$ 20

	s := &Server{
		links:              lSt,
		coupons:            cSt,
		linkPayRateLimiter: unlimitedLimiter{},
		provider:           &fakePixProvider{},
	}

	body := `{"method":"pix","coupon":"MINUS20","customer":{"name":"Ana","taxId":"12345678901"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("esperado 201, veio %d: %s", w.Code, w.Body.String())
	}
	var out Charge
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.AmountCents != 8000 {
		t.Fatalf("amountCents esperado 8000 (com desconto), veio %d", out.AmountCents)
	}
	// Verifica que IncrementCouponUse foi chamado.
	if cSt.incrementCalls.Load() != 1 {
		t.Fatalf("IncrementCouponUse deveria ter sido chamado 1x, veio %d", cSt.incrementCalls.Load())
	}
	// Verifica que used_count foi incrementado no store.
	if cSt.byID[coup.ID].UsedCount != 1 {
		t.Fatalf("usedCount esperado 1, veio %d", cSt.byID[coup.ID].UsedCount)
	}
}

func TestHandlePayViaLink_WithCouponPercent(t *testing.T) {
	// Monta: link de R$ 100, cupom percent 25% → desconto R$ 25 → charge de R$ 75.
	lSt := newFakeLinkStore()
	amount := int64(10000)
	token := linkFixture(t, lSt, &amount, []string{"pix"})

	cSt := newFakeCouponStore()
	couponFixture(t, cSt, "PCT25", "percent", 25, -1)

	s := &Server{
		links:              lSt,
		coupons:            cSt,
		linkPayRateLimiter: unlimitedLimiter{},
		provider:           &fakePixProvider{},
	}

	body := `{"method":"pix","coupon":"PCT25","customer":{"name":"Ana","taxId":"12345678901"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("esperado 201, veio %d: %s", w.Code, w.Body.String())
	}
	var out Charge
	json.Unmarshal(w.Body.Bytes(), &out)
	// 25% de 10000 = 2500 de desconto → final = 7500
	if out.AmountCents != 7500 {
		t.Fatalf("amountCents esperado 7500, veio %d", out.AmountCents)
	}
	if cSt.incrementCalls.Load() != 1 {
		t.Fatalf("IncrementCouponUse deveria ter sido chamado 1x, veio %d", cSt.incrementCalls.Load())
	}
}

func TestHandlePayViaLink_WithInvalidCoupon(t *testing.T) {
	// Cupom inexistente → 400 invalid_coupon.
	lSt := newFakeLinkStore()
	amount := int64(5000)
	token := linkFixture(t, lSt, &amount, []string{"pix"})

	cSt := newFakeCouponStore()

	s := &Server{
		links:              lSt,
		coupons:            cSt,
		linkPayRateLimiter: unlimitedLimiter{},
		provider:           &fakePixProvider{},
	}

	body := `{"method":"pix","coupon":"NAO_EXISTE","customer":{"name":"Ana","taxId":"12345678901"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para cupom inválido, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "invalid_coupon" {
		t.Fatalf("code esperado invalid_coupon, veio %q", out["code"])
	}
	if cSt.incrementCalls.Load() != 0 {
		t.Fatal("IncrementCouponUse não deveria ter sido chamado para cupom inválido")
	}
}

func TestHandlePayViaLink_WithExhaustedCoupon(t *testing.T) {
	// Cupom esgotado → 400 invalid_coupon.
	lSt := newFakeLinkStore()
	amount := int64(5000)
	token := linkFixture(t, lSt, &amount, []string{"pix"})

	cSt := newFakeCouponStore()
	c := couponFixture(t, cSt, "ESGOT", "percent", 10, 1)
	_ = cSt.IncrementCouponUse(context.Background(), c.ID) // esgota o único uso
	cSt.incrementCalls.Store(0)                            // reseta contador após setup

	s := &Server{
		links:              lSt,
		coupons:            cSt,
		linkPayRateLimiter: unlimitedLimiter{},
		provider:           &fakePixProvider{},
	}

	body := `{"method":"pix","coupon":"ESGOT","customer":{"name":"Ana","taxId":"12345678901"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para cupom esgotado, veio %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["code"] != "invalid_coupon" {
		t.Fatalf("code esperado invalid_coupon, veio %q", out["code"])
	}
}

// ── fakeDupCouponStore: força 409 na criação de cupom duplicado ──────────────
// Retorna um *pgconn.PgError com Code="23505" para disparar couponUniqueViolation.

type fakeDupCouponStore struct {
	inner *fakeCouponStore
}

func (f *fakeDupCouponStore) CreateCoupon(_ context.Context, _ Coupon) (Coupon, error) {
	return Coupon{}, &pgconn.PgError{Code: "23505"}
}

func (f *fakeDupCouponStore) ListCoupons(ctx context.Context, page listPage) ([]Coupon, error) {
	return f.inner.ListCoupons(ctx, page)
}

func (f *fakeDupCouponStore) GetCouponByCode(ctx context.Context, code string) (Coupon, error) {
	return f.inner.GetCouponByCode(ctx, code)
}

func (f *fakeDupCouponStore) SetCouponActive(ctx context.Context, id int64, active bool) error {
	return f.inner.SetCouponActive(ctx, id, active)
}

func (f *fakeDupCouponStore) RedeemCoupon(ctx context.Context, code string) (Coupon, error) {
	return f.inner.RedeemCoupon(ctx, code)
}

func (f *fakeDupCouponStore) ReleaseCouponUse(ctx context.Context, id int64) error {
	return f.inner.ReleaseCouponUse(ctx, id)
}

// TestHandlePayViaLink_CupomLimitadoSobConcorrencia é o teste do TOCTOU do cupom.
// Com max_uses=1 e 20 pagadores simultâneos, exatamente UM pode ganhar o desconto.
// Antes, o SELECT de max_uses e o incremento ficavam separados por uma chamada HTTP
// ao gateway e todos liam used_count=0 — o cupom era honrado 20 vezes.
func TestHandlePayViaLink_CupomLimitadoSobConcorrencia(t *testing.T) {
	lSt := newFakeLinkStore()
	amount := int64(10000) // R$ 100
	token := linkFixture(t, lSt, &amount, []string{"pix"})

	cSt := newFakeCouponStore()
	coup := couponFixture(t, cSt, "SO1", "fixed", 2000, 1) // R$ 20, UM único uso

	s := &Server{
		links:              lSt,
		coupons:            cSt,
		linkPayRateLimiter: unlimitedLimiter{},
		provider:           &fakePixProvider{},
	}

	const n = 20
	var comDesconto, semDesconto, recusados atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // largada simultânea, para maximizar a janela do TOCTOU
			body := `{"method":"pix","coupon":"SO1","customer":{"name":"Ana","taxId":"12345678909"}}`
			req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.SetPathValue("token", token)
			w := httptest.NewRecorder()
			s.handlePayViaLink(w, req)
			switch w.Code {
			case http.StatusCreated:
				var out Charge
				json.Unmarshal(w.Body.Bytes(), &out)
				if out.AmountCents == 8000 {
					comDesconto.Add(1)
				} else {
					semDesconto.Add(1)
				}
			case http.StatusBadRequest:
				recusados.Add(1)
			default:
				t.Errorf("status inesperado %d: %s", w.Code, w.Body.String())
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := comDesconto.Load(); got != 1 {
		t.Fatalf("cupom de uso único foi honrado %d vezes (esperado exatamente 1)", got)
	}
	if got := recusados.Load(); got != n-1 {
		t.Fatalf("esperado %d pagamentos recusados por cupom esgotado, veio %d", n-1, got)
	}
	cSt.mu.Lock()
	used := cSt.byID[coup.ID].UsedCount
	cSt.mu.Unlock()
	if used != 1 {
		t.Fatalf("usedCount deveria ficar em 1, veio %d", used)
	}
}

// TestHandlePayViaLink_CupomDevolvidoQuandoPagamentoFalha: o uso é reservado ANTES do
// gateway, então uma falha depois disso precisa devolvê-lo — senão o cupom se esgota
// sozinho a cada tentativa que não vira cobrança.
func TestHandlePayViaLink_CupomDevolvidoQuandoPagamentoFalha(t *testing.T) {
	lSt := newFakeLinkStore()
	amount := int64(10000)
	token := linkFixture(t, lSt, &amount, []string{"pix"})
	lSt.failOn = "insert_charge" // a cobrança não chega a ser gravada

	cSt := newFakeCouponStore()
	coup := couponFixture(t, cSt, "VOLTA", "fixed", 2000, 1)

	s := &Server{
		links:              lSt,
		coupons:            cSt,
		linkPayRateLimiter: unlimitedLimiter{},
		provider:           &fakePixProvider{},
	}

	body := `{"method":"pix","coupon":"VOLTA","customer":{"name":"Ana","taxId":"12345678909"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code < 400 {
		t.Fatalf("esperado erro quando a cobrança não é gravada, veio %d", w.Code)
	}
	if cSt.releaseCalls.Load() != 1 {
		t.Fatalf("o uso reservado deveria ter sido devolvido 1x, veio %d", cSt.releaseCalls.Load())
	}
	cSt.mu.Lock()
	used := cSt.byID[coup.ID].UsedCount
	cSt.mu.Unlock()
	if used != 0 {
		t.Fatalf("usedCount deveria voltar a 0, veio %d", used)
	}
}

// linkComCupons cria um link cuja lista de cupons é restritiva.
func linkComCupons(t *testing.T, st *fakeLinkStore, amount *int64, cupons []string) string {
	t.Helper()
	l := &PaymentLink{
		PublicToken: newPublicToken(),
		AmountCents: amount,
		ProductIDs:  []int{},
		Methods:     []string{"pix"},
		Coupons:     cupons,
	}
	if err := st.CreatePaymentLink(context.Background(), l); err != nil {
		t.Fatalf("linkComCupons: %v", err)
	}
	return l.PublicToken
}

// TestHandlePayViaLink_CupomForaDaListaDoLink: quando o link declara seus cupons,
// a lista é filtro. Antes, qualquer cupom ativo valia em qualquer link — um cupom de
// 90% valia num link de valor livre de R$ 10.000.
func TestHandlePayViaLink_CupomForaDaListaDoLink(t *testing.T) {
	lSt := newFakeLinkStore()
	amount := int64(10000)
	token := linkComCupons(t, lSt, &amount, []string{"DESTE_LINK"})

	cSt := newFakeCouponStore()
	coup := couponFixture(t, cSt, "DE_OUTRO", "percent", 90, -1)

	s := &Server{links: lSt, coupons: cSt, linkPayRateLimiter: unlimitedLimiter{}, provider: &fakePixProvider{}}
	body := `{"method":"pix","coupon":"DE_OUTRO","customer":{"name":"Ana","taxId":"12345678909"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("cupom fora da lista do link deveria dar 400, veio %d: %s", w.Code, w.Body.String())
	}
	// E não pode ter queimado um uso do cupom.
	cSt.mu.Lock()
	used := cSt.byID[coup.ID].UsedCount
	cSt.mu.Unlock()
	if used != 0 {
		t.Fatalf("cupom recusado não deveria consumir uso, usedCount=%d", used)
	}
}

// TestHandlePayViaLink_CupomDaListaDoLinkPassa: o filtro é case-insensitive e não
// atrapalha o cupom legítimo.
func TestHandlePayViaLink_CupomDaListaDoLinkPassa(t *testing.T) {
	lSt := newFakeLinkStore()
	amount := int64(10000)
	token := linkComCupons(t, lSt, &amount, []string{" deste_link "})

	cSt := newFakeCouponStore()
	couponFixture(t, cSt, "DESTE_LINK", "fixed", 2000, -1)

	s := &Server{links: lSt, coupons: cSt, linkPayRateLimiter: unlimitedLimiter{}, provider: &fakePixProvider{}}
	body := `{"method":"pix","coupon":"DESTE_LINK","customer":{"name":"Ana","taxId":"12345678909"}}`
	req := httptest.NewRequest("POST", "/link/"+token+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handlePayViaLink(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("cupom da lista do link deveria passar, veio %d: %s", w.Code, w.Body.String())
	}
	var out Charge
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.AmountCents != 8000 {
		t.Fatalf("esperado 8000 com desconto, veio %d", out.AmountCents)
	}
}

// TestCouponDiscount_TetoAbsoluto: o teto limita o desconto de um percentual grande
// sobre um valor grande, e o mesmo número precisa valer no preview e na cobrança.
func TestCouponDiscount_TetoAbsoluto(t *testing.T) {
	c := Coupon{DiscountType: "percent", DiscountValue: 90}
	if got := couponDiscount(c, 1_000_000, 100_000); got != 100_000 {
		t.Fatalf("90%% de R$ 10.000 deveria ser limitado ao teto de R$ 1.000, veio %d", got)
	}
	if got := couponDiscount(c, 1_000_000, 0); got != 900_000 {
		t.Fatalf("teto 0 desliga o limite, esperado 900000, veio %d", got)
	}
	// Cupom fixo maior que o teto também é limitado.
	if got := couponDiscount(Coupon{DiscountType: "fixed", DiscountValue: 500_000}, 1_000_000, 100_000); got != 100_000 {
		t.Fatalf("cupom fixo deveria respeitar o teto, veio %d", got)
	}
	// O desconto nunca passa do valor da cobrança.
	if got := couponDiscount(Coupon{DiscountType: "fixed", DiscountValue: 900}, 500, 100_000); got != 500 {
		t.Fatalf("desconto não pode passar do valor cobrado, veio %d", got)
	}
}

// TestApplyCoupon_RespeitaTeto: o preview público mostra o MESMO desconto que será
// cobrado — senão o cliente vê um valor e paga outro.
func TestApplyCoupon_RespeitaTeto(t *testing.T) {
	cSt := newFakeCouponStore()
	couponFixture(t, cSt, "META90", "percent", 90, -1)
	s := &Server{coupons: cSt, cfg: Config{CouponMaxDiscountCents: 100_000}, couponApplyRateLimiter: unlimitedLimiter{}}

	body := `{"code":"META90","amountCents":1000000}`
	req := httptest.NewRequest("POST", "/coupons/apply", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleApplyCoupon(w, req)

	var out applyCouponResult
	json.Unmarshal(w.Body.Bytes(), &out)
	if !out.Valid {
		t.Fatalf("cupom deveria ser válido: %s", w.Body.String())
	}
	if out.DiscountCents != 100_000 || out.FinalCents != 900_000 {
		t.Fatalf("preview deveria respeitar o teto (desconto 100000, final 900000), veio %d/%d", out.DiscountCents, out.FinalCents)
	}
}

// TestApplyCoupon_RateLimit: /coupons/apply é público e sem autenticação. Sem
// rate-limit, dava para varrer um dicionário de códigos à vontade.
func TestApplyCoupon_RateLimit(t *testing.T) {
	cSt := newFakeCouponStore()
	couponFixture(t, cSt, "EXISTE", "fixed", 100, -1)
	reg := newIPLimiterRegistry(rate.Every(time.Hour), 2, time.Minute)
	s := &Server{coupons: cSt, couponApplyRateLimiter: reg.For("203.0.113.50")}

	post := func(code string) int {
		body := `{"code":"` + code + `","amountCents":10000}`
		req := httptest.NewRequest("POST", "/coupons/apply", strings.NewReader(body))
		req.RemoteAddr = "203.0.113.50:1234"
		w := httptest.NewRecorder()
		s.handleApplyCoupon(w, req)
		return w.Code
	}

	if got := post("TENTA1"); got != http.StatusOK {
		t.Fatalf("1ª tentativa deveria passar, veio %d", got)
	}
	if got := post("TENTA2"); got != http.StatusOK {
		t.Fatalf("2ª tentativa deveria passar, veio %d", got)
	}
	if got := post("TENTA3"); got != http.StatusTooManyRequests {
		t.Fatalf("3ª tentativa deveria ser barrada com 429, veio %d", got)
	}
}

// TestApplyCoupon_RazaoGenerica: a resposta negativa não pode diferenciar
// "não existe" de "existe mas está inativo/esgotado" — isso é o oráculo.
func TestApplyCoupon_RazaoGenerica(t *testing.T) {
	cSt := newFakeCouponStore()
	inativo := couponFixture(t, cSt, "INATIVO2", "percent", 5, -1)
	_ = cSt.SetCouponActive(context.Background(), inativo.ID, false)
	esgotado := couponFixture(t, cSt, "ESGOTADO2", "percent", 5, 1)
	_ = cSt.IncrementCouponUse(context.Background(), esgotado.ID)
	s := buildCouponServer(cSt)

	razoes := map[string]bool{}
	for _, code := range []string{"NAO_EXISTE_MESMO", "INATIVO2", "ESGOTADO2"} {
		body := `{"code":"` + code + `","amountCents":10000}`
		req := httptest.NewRequest("POST", "/coupons/apply", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.handleApplyCoupon(w, req)
		var out applyCouponResult
		json.Unmarshal(w.Body.Bytes(), &out)
		if out.Valid {
			t.Fatalf("%s não deveria ser válido", code)
		}
		razoes[out.Reason] = true
	}
	if len(razoes) != 1 {
		t.Fatalf("as três recusas deveriam dar a MESMA razão, vieram %d razões distintas: %v", len(razoes), razoes)
	}
}
