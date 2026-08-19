package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"
)

// ── fakes de suporte ──────────────────────────────────────────────────────────

// fakeWithdrawalStore implementa withdrawalStore sem banco.
type fakeWithdrawalStore struct {
	created []Withdrawal
	listErr error
}

func (f *fakeWithdrawalStore) CreateWithdrawal(_ context.Context, w *Withdrawal) error {
	w.ID = int64(len(f.created) + 1)
	w.CreatedAt = time.Now()
	f.created = append(f.created, *w)
	return nil
}

func (f *fakeWithdrawalStore) ListWithdrawals(_ context.Context, _ listPage) ([]Withdrawal, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]Withdrawal, len(f.created))
	copy(out, f.created)
	return out, nil
}

func (f *fakeWithdrawalStore) GetWithdrawalByIdempotencyKey(_ context.Context, key string) (*Withdrawal, error) {
	for i := range f.created {
		if f.created[i].IdempotencyKey == key {
			w := f.created[i]
			return &w, nil
		}
	}
	return nil, nil
}

// fakeEfiOpsBalance implementa efiOps devolvendo saldo configurável.
// Embute fakeEfiOps (definido em handlers_efi_test.go) para reusar os demais métodos.
type fakeEfiOpsBalance struct {
	fakeEfiOps
	balanceCents int64
	balanceErr   error
	// Configuração do SendPix (payout): valores devolvidos e erro opcional. Quando
	// sendErr != nil, SendPix falha (simula erro do gateway). Captura de argumentos
	// recebidos para asserções.
	sendIDEnvio    string
	sendE2EID      string
	sendStatus     string
	sendErr        error
	gotSendIDEnvio string
	gotSendCents   int64
	gotSendFav     string
	gotSendInfo    string
	sendCalls      int
}

func (f fakeEfiOpsBalance) GetBalance(_ context.Context) (int64, error) {
	return f.balanceCents, f.balanceErr
}

// SendPix de fakeEfiOpsBalance (ponteiro p/ capturar args). Sobrepõe o de fakeEfiOps.
func (f *fakeEfiOpsBalance) SendPix(_ context.Context, idEnvio string, valorCents int64, fav, info string) (string, string, string, error) {
	f.sendCalls++
	f.gotSendIDEnvio = idEnvio
	f.gotSendCents = valorCents
	f.gotSendFav = fav
	f.gotSendInfo = info
	if f.sendErr != nil {
		return "", "", "", f.sendErr
	}
	out := f.sendIDEnvio
	if out == "" {
		out = idEnvio
	}
	status := f.sendStatus
	if status == "" {
		status = "EM_PROCESSAMENTO"
	}
	return out, f.sendE2EID, status, nil
}

// unlimitedLimiter é um rateLimiterIface que nunca limita (burst infinito).
type unlimitedLimiter struct{}

func (unlimitedLimiter) Allow() bool { return true }

// blockedLimiter é um rateLimiterIface que sempre bloqueia (simula limite atingido).
type blockedLimiter struct{}

func (blockedLimiter) Allow() bool { return false }

// ── helpers de token ──────────────────────────────────────────────────────────

const testPayoutSecret = "test-secret-payout-c3"

// makeSudoTok cria um JWT HS256 com sudo_exp relativo ao momento atual.
// sudoOffset positivo = sudo válido; negativo = sudo expirado.
func makeSudoTok(secret string, sudoOffset time.Duration) string {
	claims := jwt.MapClaims{
		"sub":      "1",
		"iat":      time.Now().Unix(),
		"exp":      time.Now().Add(time.Hour).Unix(),
		"sudo_exp": time.Now().Add(sudoOffset).Unix(),
	}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	return tok
}

// makeRegularTok cria um JWT HS256 sem o claim sudo_exp.
func makeRegularTok(secret string) string {
	claims := jwt.MapClaims{
		"sub": "1",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	return tok
}

// newPayoutReq monta um *http.Request para POST /efi/payout com o token no header.
func newPayoutReq(token string, body map[string]any) *http.Request {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/efi/payout", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// basePayoutServer constrói um Server mínimo para testes de payout.
// rateLim: nil usa unlimitedLimiter (nunca bloqueia).
func basePayoutServer(balanceCents int64, payoutEnabled bool, rateLim rateLimiterIface) *Server {
	lim := rateLimiterIface(unlimitedLimiter{})
	if rateLim != nil {
		lim = rateLim
	}
	return &Server{
		cfg: Config{
			JWTSecret:     testPayoutSecret,
			PayoutEnabled: payoutEnabled,
		},
		efi:               &fakeEfiOpsBalance{balanceCents: balanceCents},
		payout:            &fakeWithdrawalStore{},
		payoutRateLimiter: lim,
	}
}

// ── Testes obrigatórios da spec C3.2 ─────────────────────────────────────────

// TestPayout_SemSudo: token sem sudo_exp → 403 sudo_required.
func TestPayout_SemSudo(t *testing.T) {
	s := basePayoutServer(100_000, false, nil)
	tok := makeRegularTok(testPayoutSecret)

	w := httptest.NewRecorder()
	s.handlePayout(w, newPayoutReq(tok, map[string]any{
		"amountCents": 5000,
		"destination": "12345-6",
	}))

	if w.Code != http.StatusForbidden {
		t.Fatalf("esperado 403, veio %d: %s", w.Code, w.Body.String())
	}
	assertCode(t, w.Body.Bytes(), "sudo_required")
}

// TestPayout_SudoExpirado: sudo_exp no passado → 403 sudo_required.
func TestPayout_SudoExpirado(t *testing.T) {
	s := basePayoutServer(100_000, false, nil)
	tok := makeSudoTok(testPayoutSecret, -1*time.Minute)

	w := httptest.NewRecorder()
	s.handlePayout(w, newPayoutReq(tok, map[string]any{
		"amountCents": 5000,
		"destination": "12345-6",
	}))

	if w.Code != http.StatusForbidden {
		t.Fatalf("esperado 403, veio %d: %s", w.Code, w.Body.String())
	}
	assertCode(t, w.Body.Bytes(), "sudo_required")
}

// TestPayout_ValorAcimaDoSaldo: amountCents > saldo disponível → 400 insufficient_balance.
func TestPayout_ValorAcimaDoSaldo(t *testing.T) {
	s := basePayoutServer(10_000, false, nil) // saldo: R$ 100,00
	tok := makeSudoTok(testPayoutSecret, 15*time.Minute)

	w := httptest.NewRecorder()
	s.handlePayout(w, newPayoutReq(tok, map[string]any{
		"amountCents":    20_000, // R$ 200,00 > R$ 100,00
		"destination":    "12345-6",
		"idempotencyKey": "idem-saldo-test",
	}))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400, veio %d: %s", w.Code, w.Body.String())
	}
	assertCode(t, w.Body.Bytes(), "insufficient_balance")
}

// TestPayout_RateLimit: limiter bloqueado → 429 rate_limited.
func TestPayout_RateLimit(t *testing.T) {
	// Injetamos um limiter que sempre bloqueia (sem depender do limiter global).
	s := basePayoutServer(100_000, false, blockedLimiter{})
	tok := makeSudoTok(testPayoutSecret, 15*time.Minute)

	w := httptest.NewRecorder()
	s.handlePayout(w, newPayoutReq(tok, map[string]any{
		"amountCents": 5000,
		"destination": "12345-6",
	}))

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("esperado 429, veio %d: %s", w.Code, w.Body.String())
	}
	assertCode(t, w.Body.Bytes(), "rate_limited")
}

// TestPayout_FlagDesligada: sudo válido + valor ≤ saldo + PAYOUT_ENABLED=false → 503 payout_disabled.
// Verifica que todos os guards passaram mas nenhuma transferência foi executada.
func TestPayout_FlagDesligada(t *testing.T) {
	ws := &fakeWithdrawalStore{}
	s := &Server{
		cfg: Config{
			JWTSecret:      testPayoutSecret,
			PayoutEnabled:  false, // FLAG DESLIGADA
			PayoutMaxCents: 100_000,
		},
		efi:               &fakeEfiOpsBalance{balanceCents: 100_000},
		payout:            ws,
		payoutRateLimiter: unlimitedLimiter{},
	}
	tok := makeSudoTok(testPayoutSecret, 15*time.Minute)

	w := httptest.NewRecorder()
	s.handlePayout(w, newPayoutReq(tok, map[string]any{
		"amountCents":    5000, // < 100_000 → saldo OK
		"destination":    "12345-6",
		"idempotencyKey": "idem-flag-off-test",
	}))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("esperado 503, veio %d: %s", w.Code, w.Body.String())
	}
	assertCode(t, w.Body.Bytes(), "payout_disabled")

	// Nenhum withdrawal deve ter sido criado.
	if len(ws.created) != 0 {
		t.Fatalf("esperado 0 withdrawals criados com flag OFF, veio %d", len(ws.created))
	}
}

// ── Testes adicionais de validação ───────────────────────────────────────────

// TestPayout_AmountZero: amountCents=0 → 400 invalid_amount.
func TestPayout_AmountZero(t *testing.T) {
	s := basePayoutServer(100_000, false, nil)
	tok := makeSudoTok(testPayoutSecret, 15*time.Minute)

	w := httptest.NewRecorder()
	s.handlePayout(w, newPayoutReq(tok, map[string]any{
		"amountCents":    0,
		"destination":    "12345-6",
		"idempotencyKey": "idem-zero-test",
	}))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400, veio %d: %s", w.Code, w.Body.String())
	}
	assertCode(t, w.Body.Bytes(), "invalid_amount")
}

// TestPayout_SemDestino: destination vazio → 400 invalid_destination.
func TestPayout_SemDestino(t *testing.T) {
	s := basePayoutServer(100_000, false, nil)
	tok := makeSudoTok(testPayoutSecret, 15*time.Minute)

	w := httptest.NewRecorder()
	s.handlePayout(w, newPayoutReq(tok, map[string]any{
		"amountCents":    5000,
		"destination":    "",
		"idempotencyKey": "idem-dest-test",
	}))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400, veio %d: %s", w.Code, w.Body.String())
	}
	assertCode(t, w.Body.Bytes(), "invalid_destination")
}

// TestPayout_DestinoNaoExposto: destination nunca deve aparecer no corpo da resposta.
func TestPayout_DestinoNaoExposto(t *testing.T) {
	destSensivel := "CONTA-BANCARIA-SECRETA-99887766"
	s := basePayoutServer(100_000, false, nil) // flag OFF → 503
	tok := makeSudoTok(testPayoutSecret, 15*time.Minute)

	w := httptest.NewRecorder()
	s.handlePayout(w, newPayoutReq(tok, map[string]any{
		"amountCents":    5000,
		"destination":    destSensivel,
		"idempotencyKey": "idem-dest-exposed-test",
	}))

	if strings.Contains(w.Body.String(), destSensivel) {
		t.Fatalf("SEGURANÇA: destination sensível encontrado no corpo da resposta: %s", w.Body.String())
	}
}

// TestPayout_WithdrawalNaoContemDestination: o Withdrawal serializado não deve conter o campo destination.
func TestPayout_WithdrawalNaoContemDestination(t *testing.T) {
	ws := &fakeWithdrawalStore{}
	s := &Server{
		cfg: Config{
			JWTSecret:      testPayoutSecret,
			PayoutEnabled:  true, // flag ON → cria withdrawal
			PayoutMaxCents: 100_000,
		},
		efi:               &fakeEfiOpsBalance{balanceCents: 100_000},
		payout:            ws,
		payoutRateLimiter: unlimitedLimiter{},
	}
	tok := makeSudoTok(testPayoutSecret, 15*time.Minute)
	destSensivel := "CHAVE-PIX-CONFIDENCIAL"

	w := httptest.NewRecorder()
	s.handlePayout(w, newPayoutReq(tok, map[string]any{
		"amountCents":    5000,
		"destination":    destSensivel,
		"idempotencyKey": "idem-dest-nao-exposto-test",
	}))

	if w.Code != http.StatusAccepted {
		t.Fatalf("esperado 202, veio %d: %s", w.Code, w.Body.String())
	}

	// destination não pode aparecer no JSON de resposta.
	if strings.Contains(w.Body.String(), destSensivel) {
		t.Fatalf("SEGURANÇA: destination sensível no corpo da resposta: %s", w.Body.String())
	}

	// withdrawal criado não deve ter destination salvo.
	if len(ws.created) != 1 {
		t.Fatalf("esperado 1 withdrawal criado, veio %d", len(ws.created))
	}
	if ws.created[0].Destination != "" {
		t.Fatalf("SEGURANÇA: Withdrawal.Destination não deveria estar preenchido: %q", ws.created[0].Destination)
	}
}

// TestListWithdrawals_Vazio: GET /withdrawals sem saques → 200 array vazio.
func TestListWithdrawals_Vazio(t *testing.T) {
	s := &Server{payout: &fakeWithdrawalStore{}}
	w := httptest.NewRecorder()
	s.handleListWithdrawals(w, httptest.NewRequest(http.MethodGet, "/withdrawals", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var out []Withdrawal
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("esperado 0 saques, veio %d", len(out))
	}
}

// TestEfiBalance_Detalhado: GET /efi/balance retorna EfiBalance com os 5 campos.
func TestEfiBalance_Detalhado(t *testing.T) {
	s := &Server{efi: &fakeEfiOpsBalance{balanceCents: 50_000}}
	w := httptest.NewRecorder()
	s.handleEfiBalance(w, httptest.NewRequest(http.MethodGet, "/efi/balance", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var out EfiBalance
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.AvailableCents != 50_000 {
		t.Fatalf("availableCents=%d, esperado 50000", out.AvailableCents)
	}
	// Campos sem endpoint Efí → sempre 0 (anotados como TODO no handler).
	if out.ProcessingCents != 0 || out.BlockedCents != 0 ||
		out.DailyLimitCents != 0 || out.NightLimitCents != 0 {
		t.Fatalf("campos sem endpoint Efí devem ser 0: processing=%d blocked=%d daily=%d night=%d",
			out.ProcessingCents, out.BlockedCents, out.DailyLimitCents, out.NightLimitCents)
	}
}

// TestPayout_RateLimitGlobal: verifica que o limiter global (payoutDefaultLimiter)
// usa rate.Every(10s) com burst=1 — burst=1 fecha a janela de double-submit.
func TestPayout_RateLimitGlobal(t *testing.T) {
	// Verifica o burst do limiter padrão (não altera o estado global).
	burst := payoutDefaultLimiter.Burst()
	if burst != 1 {
		t.Fatalf("burst esperado 1, veio %d", burst)
	}
	// Verifica que a taxa é compatível com 1/10s (limite ≥ 0 e ≤ 1/5s).
	limRate := float64(payoutDefaultLimiter.Limit())
	if limRate <= 0 || limRate > 0.2 { // 0.1/s = 1/10s
		t.Fatalf("rate esperada ~0.1 req/s, veio %f", limRate)
	}
}

// TestPayout_SemIdempotencyKey: sem idempotencyKey → 400 idempotency_key_required.
func TestPayout_SemIdempotencyKey(t *testing.T) {
	s := basePayoutServer(100_000, false, nil)
	tok := makeSudoTok(testPayoutSecret, 15*time.Minute)

	w := httptest.NewRecorder()
	s.handlePayout(w, newPayoutReq(tok, map[string]any{
		"amountCents": 5000,
		"destination": "12345-6",
		// idempotencyKey ausente
	}))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 sem idempotencyKey, veio %d: %s", w.Code, w.Body.String())
	}
	assertCode(t, w.Body.Bytes(), "idempotency_key_required")
}

// TestPayout_IdempotencyKeyDuplica: mesma idempotencyKey 2x → apenas 1 withdrawal criado.
func TestPayout_IdempotencyKeyDuplica(t *testing.T) {
	ws := &fakeWithdrawalStore{}
	s := &Server{
		cfg: Config{
			JWTSecret:      testPayoutSecret,
			PayoutEnabled:  true,
			PayoutMaxCents: 100_000,
		},
		efi:               &fakeEfiOpsBalance{balanceCents: 100_000},
		payout:            ws,
		payoutRateLimiter: unlimitedLimiter{},
	}
	tok := makeSudoTok(testPayoutSecret, 15*time.Minute)
	body := map[string]any{
		"amountCents":    5000,
		"destination":    "chave-pix",
		"idempotencyKey": "key-unica-abc123",
	}

	// 1ª requisição: cria o withdrawal.
	w1 := httptest.NewRecorder()
	s.handlePayout(w1, newPayoutReq(tok, body))
	if w1.Code != http.StatusAccepted {
		t.Fatalf("1ª req: esperado 202, veio %d: %s", w1.Code, w1.Body.String())
	}

	// 2ª requisição com a mesma chave: deve devolver o existente sem criar outro.
	w2 := httptest.NewRecorder()
	s.handlePayout(w2, newPayoutReq(tok, body))
	if w2.Code != http.StatusOK {
		t.Fatalf("2ª req (duplicada): esperado 200, veio %d: %s", w2.Code, w2.Body.String())
	}

	// Apenas 1 withdrawal deve ter sido criado.
	if len(ws.created) != 1 {
		t.Fatalf("esperado 1 withdrawal após 2 requests com mesma key, veio %d", len(ws.created))
	}
}

// TestPayout_ValorAcimaDoMaximo: amountCents > PAYOUT_MAX_CENTS → 400 amount_too_large.
func TestPayout_ValorAcimaDoMaximo(t *testing.T) {
	ws := &fakeWithdrawalStore{}
	s := &Server{
		cfg: Config{
			JWTSecret:      testPayoutSecret,
			PayoutEnabled:  false,
			PayoutMaxCents: 50_000, // R$ 500,00
		},
		efi:               &fakeEfiOpsBalance{balanceCents: 999_999},
		payout:            ws,
		payoutRateLimiter: unlimitedLimiter{},
	}
	tok := makeSudoTok(testPayoutSecret, 15*time.Minute)

	w := httptest.NewRecorder()
	s.handlePayout(w, newPayoutReq(tok, map[string]any{
		"amountCents":    50_001, // 1 centavo acima do limite
		"destination":    "chave-pix",
		"idempotencyKey": "idem-max-test",
	}))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400 para valor acima do máximo, veio %d: %s", w.Code, w.Body.String())
	}
	assertCode(t, w.Body.Bytes(), "amount_too_large")
	// Nenhum withdrawal criado.
	if len(ws.created) != 0 {
		t.Fatalf("esperado 0 withdrawals criados, veio %d", len(ws.created))
	}
}

// TestPayout_FlagLigada_ChamaEfiECriaWithdrawal: PAYOUT_ENABLED=true + Efí 200 →
// SendPix chamado com os args certos; withdrawal criado com idEnvio/e2eId e status
// "processing" (EM_PROCESSAMENTO mapeado). Status 202.
func TestPayout_FlagLigada_ChamaEfiECriaWithdrawal(t *testing.T) {
	ws := &fakeWithdrawalStore{}
	efi := &fakeEfiOpsBalance{
		balanceCents: 100_000,
		sendIDEnvio:  "IDENVIO-OUT-1",
		sendE2EID:    "E2E-OUT-1",
		sendStatus:   "EM_PROCESSAMENTO",
	}
	s := &Server{
		cfg: Config{
			JWTSecret:      testPayoutSecret,
			PayoutEnabled:  true,
			PayoutMaxCents: 100_000,
		},
		efi:               efi,
		payout:            ws,
		payoutRateLimiter: unlimitedLimiter{},
	}
	tok := makeSudoTok(testPayoutSecret, 15*time.Minute)

	w := httptest.NewRecorder()
	s.handlePayout(w, newPayoutReq(tok, map[string]any{
		"amountCents":    7500,
		"destination":    "chave-pix-destino@ex.com",
		"idempotencyKey": "Idem-Key-Abc-123", // com hífens — sanitizado no SendPix real
	}))

	if w.Code != http.StatusAccepted {
		t.Fatalf("esperado 202, veio %d: %s", w.Code, w.Body.String())
	}
	if efi.sendCalls != 1 {
		t.Fatalf("SendPix chamado %d vezes, esperado 1", efi.sendCalls)
	}
	// Args repassados à Efí.
	if efi.gotSendCents != 7500 {
		t.Fatalf("valorCents repassado: %d, esperado 7500", efi.gotSendCents)
	}
	if efi.gotSendFav != "chave-pix-destino@ex.com" {
		t.Fatalf("favorecidoChave: %q", efi.gotSendFav)
	}
	if efi.gotSendIDEnvio != "Idem-Key-Abc-123" {
		t.Fatalf("idEnvio passado ao provider: %q (sanitização ocorre dentro do provider)", efi.gotSendIDEnvio)
	}
	// Withdrawal criado com idEnvio/e2eId e status processing.
	if len(ws.created) != 1 {
		t.Fatalf("esperado 1 withdrawal, veio %d", len(ws.created))
	}
	got := ws.created[0]
	if got.Status != "processing" {
		t.Fatalf("status esperado processing, veio %q", got.Status)
	}
	if got.EFIIdEnvio != "IDENVIO-OUT-1" || got.E2EID != "E2E-OUT-1" {
		t.Fatalf("idEnvio/e2eId no withdrawal: %q/%q", got.EFIIdEnvio, got.E2EID)
	}
}

// TestPayout_FlagLigada_ErroEfi: SendPix falha → 502 payout_failed e nenhum withdrawal criado.
func TestPayout_FlagLigada_ErroEfi(t *testing.T) {
	ws := &fakeWithdrawalStore{}
	efi := &fakeEfiOpsBalance{
		balanceCents: 100_000,
		sendErr:      errors.New("gateway recusou (não deve vazar)"),
	}
	s := &Server{
		cfg: Config{
			JWTSecret:      testPayoutSecret,
			PayoutEnabled:  true,
			PayoutMaxCents: 100_000,
		},
		efi:               efi,
		payout:            ws,
		payoutRateLimiter: unlimitedLimiter{},
	}
	tok := makeSudoTok(testPayoutSecret, 15*time.Minute)

	w := httptest.NewRecorder()
	s.handlePayout(w, newPayoutReq(tok, map[string]any{
		"amountCents":    5000,
		"destination":    "chave-pix",
		"idempotencyKey": "idem-erro-efi",
	}))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("esperado 502, veio %d: %s", w.Code, w.Body.String())
	}
	assertCode(t, w.Body.Bytes(), "payout_failed")
	// A mensagem crua do gateway NÃO pode vazar no corpo.
	if strings.Contains(w.Body.String(), "não deve vazar") {
		t.Fatalf("SEGURANÇA: mensagem crua do gateway vazou: %s", w.Body.String())
	}
	// Nenhum withdrawal registrado (transferência não confirmada).
	if len(ws.created) != 0 {
		t.Fatalf("esperado 0 withdrawals em erro da Efí, veio %d", len(ws.created))
	}
}

// TestPayout_FlagDesligada_NaoChamaEfi: PAYOUT_ENABLED=false → 503 e SendPix NÃO é chamado.
func TestPayout_FlagDesligada_NaoChamaEfi(t *testing.T) {
	ws := &fakeWithdrawalStore{}
	efi := &fakeEfiOpsBalance{balanceCents: 100_000}
	s := &Server{
		cfg: Config{
			JWTSecret:      testPayoutSecret,
			PayoutEnabled:  false,
			PayoutMaxCents: 100_000,
		},
		efi:               efi,
		payout:            ws,
		payoutRateLimiter: unlimitedLimiter{},
	}
	tok := makeSudoTok(testPayoutSecret, 15*time.Minute)

	w := httptest.NewRecorder()
	s.handlePayout(w, newPayoutReq(tok, map[string]any{
		"amountCents":    5000,
		"destination":    "chave-pix",
		"idempotencyKey": "idem-flag-off-noefi",
	}))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("esperado 503, veio %d: %s", w.Code, w.Body.String())
	}
	assertCode(t, w.Body.Bytes(), "payout_disabled")
	if efi.sendCalls != 0 {
		t.Fatalf("SendPix NÃO deveria ser chamado com flag OFF, veio %d chamadas", efi.sendCalls)
	}
}

// ── helper interno ────────────────────────────────────────────────────────────

// assertCode verifica o campo "code" no JSON de resposta.
func assertCode(t *testing.T, body []byte, expected string) {
	t.Helper()
	var out map[string]string
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal de erro: %v — body: %s", err, body)
	}
	if out["code"] != expected {
		t.Fatalf("code esperado %q, veio %q — body: %s", expected, out["code"], body)
	}
}

// [Fix 11] handleListWithdrawals com store nil → 503 db_unavailable (não 200 []).
func TestListWithdrawals_StoreNil(t *testing.T) {
	s := &Server{} // payout=nil, store=nil → withdrawalStoreOf() retorna nil
	w := httptest.NewRecorder()
	s.handleListWithdrawals(w, httptest.NewRequest(http.MethodGet, "/withdrawals", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("[Fix11] esperado 503, veio %d: %s", w.Code, w.Body.String())
	}
	assertCode(t, w.Body.Bytes(), "db_unavailable")
}

// Garante que rate.NewLimiter está em uso (evita "imported and not used").
var _ = rate.NewLimiter
