package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// Cobrem somente os caminhos de validação que retornam ANTES de tocar no
// banco/Redis (corpo inválido, challenge mal-formado) — CI sem Postgres/Redis.

func TestHandleMFAEmailBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleMFAEmail(w, httptest.NewRequest("POST", "/auth/mfa/email", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400)", w.Code)
	}
}

// TestHandleMFAEmailInvalidChallenge: challenge de formato errado é rejeitado
// por isValidChallenge antes de qualquer chamada ao Redis.
func TestHandleMFAEmailInvalidChallenge(t *testing.T) {
	s := testServer(Config{})
	for _, bad := range []string{
		"",
		"curto",
		strings.Repeat("a", 47), // 1 char a menos
		strings.Repeat("a", 49), // 1 char a mais
		strings.Repeat("x", 48), // comprimento certo mas 'x' não é hex
	} {
		w := httptest.NewRecorder()
		s.handleMFAEmail(w, httptest.NewRequest("POST", "/auth/mfa/email",
			strings.NewReader(`{"challenge":"`+bad+`"}`)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("challenge %q: code=%d (queria 400)", bad, w.Code)
		}
	}
}

// TestHandleMFAEmailMissingChallenge: formato correto mas ausente no Redis → 400.
func TestHandleMFAEmailMissingChallenge(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	w := httptest.NewRecorder()
	s.handleMFAEmail(w, httptest.NewRequest("POST", "/auth/mfa/email",
		strings.NewReader(`{"challenge":"`+strings.Repeat("a", 48)+`"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("challenge ausente no Redis: code=%d (queria 400)", w.Code)
	}
}

func TestHandleMFAVerifyBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleMFAVerify(w, httptest.NewRequest("POST", "/auth/mfa/verify", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400)", w.Code)
	}
}

// TestHandleMFAVerifyInvalidChallenge: challenge mal-formado é rejeitado por
// isValidChallenge antes de Incr no Redis (proteção de tentativas).
func TestHandleMFAVerifyInvalidChallenge(t *testing.T) {
	s := testServer(Config{})
	for _, bad := range []string{
		"",
		"x",
		strings.Repeat("a", 47),
		strings.Repeat("x", 48), // não é hex válido
	} {
		w := httptest.NewRecorder()
		s.handleMFAVerify(w, httptest.NewRequest("POST", "/auth/mfa/verify",
			strings.NewReader(`{"challenge":"`+bad+`","code":"123456"}`)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("challenge %q: code=%d (queria 400)", bad, w.Code)
		}
	}
}

// TestHandleMFAVerifyMissingChallenge: formato válido mas ausente no Redis → 400
// (challengeUser devolve false sem incrementar o contador de tentativas).
func TestHandleMFAVerifyMissingChallenge(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	w := httptest.NewRecorder()
	s.handleMFAVerify(w, httptest.NewRequest("POST", "/auth/mfa/verify",
		strings.NewReader(`{"challenge":"`+strings.Repeat("b", 48)+`","code":"123456"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("challenge ausente no Redis: code=%d (queria 400)", w.Code)
	}
}

func TestHandleMFAEnableBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleMFAEnable(w, httptest.NewRequest("POST", "/auth/mfa/enable", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400)", w.Code)
	}
}

func TestHandleMFADisableBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleMFADisable(w, httptest.NewRequest("POST", "/auth/mfa/disable", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400)", w.Code)
	}
}

// TestHandleMFAVerifyInvalidCodeLength: código com comprimento fora de 6 (OTP/TOTP) ou
// recoveryCodeLen (13 chars) é rejeitado antes de qualquer chamada ao Redis ou banco.
func TestHandleMFAVerifyInvalidCodeLength(t *testing.T) {
	s := testServer(Config{})
	validChallenge := strings.Repeat("a", 48)
	for _, bad := range []string{
		"",
		"12345",                                // 5 chars
		"1234567",                              // 7 chars
		strings.Repeat("a", recoveryCodeLen-1), // 12 chars
		strings.Repeat("a", recoveryCodeLen+1), // 14 chars
	} {
		w := httptest.NewRecorder()
		s.handleMFAVerify(w, httptest.NewRequest("POST", "/auth/mfa/verify",
			strings.NewReader(`{"challenge":"`+validChallenge+`","code":"`+bad+`"}`)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("code=%q (len=%d): status=%d (queria 400)", bad, len(bad), w.Code)
		}
	}
}

// TestHandleMFADisableInvalidCodeLength: código com comprimento fora do esperado é
// rejeitado logo após o parse do corpo, sem tocar no banco ou Redis.
func TestHandleMFADisableInvalidCodeLength(t *testing.T) {
	s := testServer(Config{})
	for _, bad := range []string{
		"",
		"12345",                                // 5 chars
		"1234567",                              // 7 chars
		strings.Repeat("a", recoveryCodeLen-1), // 12 chars
		strings.Repeat("a", recoveryCodeLen+1), // 14 chars
	} {
		w := httptest.NewRecorder()
		r := reqAs(httptest.NewRequest("POST", "/auth/mfa/disable",
			strings.NewReader(`{"code":"`+bad+`"}`)), 42)
		s.handleMFADisable(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("code=%q (len=%d): status=%d (queria 400)", bad, len(bad), w.Code)
		}
	}
}

func TestHandleMFAEmailEnableBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleMFAEmailEnable(w, httptest.NewRequest("POST", "/auth/mfa/email-enable", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400)", w.Code)
	}
}

// TestHandleMFAEmailEnableInvalidCodeLength: código com comprimento ≠ 6 é rejeitado
// antes do contador de tentativas e de qualquer chamada ao Redis ou banco.
func TestHandleMFAEmailEnableInvalidCodeLength(t *testing.T) {
	s := testServer(Config{})
	for _, bad := range []string{
		"",
		"12345",   // 5 chars
		"1234567", // 7 chars
	} {
		w := httptest.NewRecorder()
		r := reqAs(httptest.NewRequest("POST", "/auth/mfa/email-enable",
			strings.NewReader(`{"code":"`+bad+`"}`)), 42)
		s.handleMFAEmailEnable(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("code=%q (len=%d): status=%d (queria 400)", bad, len(bad), w.Code)
		}
	}
}

// TestHandleMFAEmailEnableTooManyAttempts: após 5 tentativas (pré-semeadas no Redis),
// a 6ª deve retornar 429 TOO_MANY_ATTEMPTS antes de qualquer consulta ao banco.
func TestHandleMFAEmailEnableTooManyAttempts(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	// Simula 5 tentativas anteriores para uid=42 diretamente no Redis
	if err := s.rdb.Set(context.Background(), "api-go:mfa_email_enable_attempts:42", "5", 0).Err(); err != nil {
		t.Fatalf("set attempt counter: %v", err)
	}
	w := httptest.NewRecorder()
	r := reqAs(httptest.NewRequest("POST", "/auth/mfa/email-enable",
		strings.NewReader(`{"code":"123456"}`)), 42)
	s.handleMFAEmailEnable(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("6ª tentativa: code=%d (queria 429)", w.Code)
	}
}

// TestHandleMFAEmailEnableRedisDown garante que handleMFAEmailEnable retorna 500
// (fail-closed) quando o Redis está indisponível, e não deixa a requisição
// prosseguir sem confirmar o contador de tentativas. O OTP de email (6 dígitos,
// 10⁶ combinações) seria brute-forçável durante uma queda do Redis sem esta proteção.
// O Redis é consultado ANTES do banco neste handler, então a ausência de banco real
// não interfere — a falha ocorre no Incr do contador de tentativas.
func TestHandleMFAEmailEnableRedisDown(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })

	mr.SetError("ERR simulated Redis failure")

	w := httptest.NewRecorder()
	r := reqAs(httptest.NewRequest("POST", "/auth/mfa/email-enable",
		strings.NewReader(`{"code":"123456"}`)), 42)
	s.handleMFAEmailEnable(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("Redis indisponível deve retornar 500 (fail-closed), veio %d", w.Code)
	}
}

// TestHandleMFAEmailChallengeRedisDown garante que handleMFAEmail retorna 500
// (fail-closed) quando o Redis está indisponível ao verificar o challenge.
// Sem esta proteção, a falha seria silenciada como INVALID_CHALLENGE (400),
// mascarando uma indisponibilidade de infraestrutura.
func TestHandleMFAEmailChallengeRedisDown(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })

	// Desliga o Redis antes da requisição chegar ao handler.
	mr.SetError("ERR simulated Redis failure")

	w := httptest.NewRecorder()
	s.handleMFAEmail(w, httptest.NewRequest("POST", "/auth/mfa/email",
		strings.NewReader(`{"challenge":"`+strings.Repeat("a", 48)+`"}`)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("Redis indisponível deve retornar 500 (fail-closed), veio %d", w.Code)
	}
}

// TestHandleMFAVerifyChallengeRedisDown garante que handleMFAVerify retorna 500
// (fail-closed) quando o Redis está indisponível ao verificar o challenge.
func TestHandleMFAVerifyChallengeRedisDown(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })

	mr.SetError("ERR simulated Redis failure")

	w := httptest.NewRecorder()
	s.handleMFAVerify(w, httptest.NewRequest("POST", "/auth/mfa/verify",
		strings.NewReader(`{"challenge":"`+strings.Repeat("a", 48)+`","code":"123456"}`)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("Redis indisponível deve retornar 500 (fail-closed), veio %d", w.Code)
	}
}

func TestHandleMFAMethodBadBody(t *testing.T) {
	s := testServer(Config{})
	w := httptest.NewRecorder()
	s.handleMFAMethod(w, httptest.NewRequest("POST", "/auth/mfa/method", strings.NewReader("não-é-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d (queria 400)", w.Code)
	}
}

// TestConsumeAcctEmailCodeDeletesKeyOnMismatch documenta o comportamento de
// consumeAcctEmailCode: ela usa GetDel e deleta a chave do Redis mesmo quando
// o código não bate. Este é o motivo pelo qual handleMFADisable e handleSudoVerify
// NÃO devem chamar consumeAcctEmailCode para códigos de comprimento recoveryCodeLen
// (13 chars) — fazer isso invalidaria silenciosamente qualquer OTP de email pendente
// sem que o usuário tivesse tentado usá-lo.
func TestConsumeAcctEmailCodeDeletesKeyOnMismatch(t *testing.T) {
	s := testServerWithRedis(t, Config{})

	const uid = int64(42)
	key := mfaEmailAcctKey(uid)
	// Semeia um OTP de 6 dígitos válido para a conta
	if err := s.rdb.Set(context.Background(), key, "123456", 0).Err(); err != nil {
		t.Fatalf("set email OTP: %v", err)
	}

	// Código errado (comprimento de recovery code): consumeAcctEmailCode retorna
	// false, mas o GetDel já removeu a chave atomicamente.
	if s.consumeAcctEmailCode(context.Background(), uid, strings.Repeat("A", recoveryCodeLen)) {
		t.Fatal("código de recovery code não deve ser aceito como OTP de email")
	}

	// A chave foi deletada mesmo sem match — é por isso que os handlers devem
	// pular consumeAcctEmailCode quando len(code) == recoveryCodeLen.
	exists, err := s.rdb.Exists(context.Background(), key).Result()
	if err != nil {
		t.Fatalf("redis exists: %v", err)
	}
	if exists != 0 {
		t.Fatal("consumeAcctEmailCode usa GetDel: a chave devia ter sido deletada mesmo sem match")
	}
}

// TestGenRecoveryCodesLen verifica que os códigos gerados têm exatamente
// recoveryCodeLen caracteres — mesma constante usada para filtrar a consulta ao banco.
func TestGenRecoveryCodesLen(t *testing.T) {
	codes := genRecoveryCodes(10)
	if len(codes) != 10 {
		t.Fatalf("queria 10 códigos, got %d", len(codes))
	}
	for i, c := range codes {
		if len(c) != recoveryCodeLen {
			t.Errorf("código[%d] = %q: comprimento %d (queria %d)", i, c, len(c), recoveryCodeLen)
		}
	}
}

// TestHandleMFAEnableSetupExpired: chave mfa_setup ausente no Redis (setup expirado
// ou não iniciado) deve retornar 400 MFA_SETUP_EXPIRED antes de qualquer Incr.
func TestHandleMFAEnableSetupExpired(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	w := httptest.NewRecorder()
	r := reqAs(httptest.NewRequest("POST", "/auth/mfa/enable",
		strings.NewReader(`{"code":"123456"}`)), 42)
	s.handleMFAEnable(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("setup ausente: code=%d (queria 400)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "MFA_SETUP_EXPIRED") {
		t.Errorf("esperava MFA_SETUP_EXPIRED no corpo, veio: %s", w.Body.String())
	}
}

// TestHandleMFAEnableTooManyAttempts: após 5 tentativas (pré-semeadas no Redis),
// a 6ª deve retornar 429 TOO_MANY_ATTEMPTS e apagar mfa_setup + contador.
func TestHandleMFAEnableTooManyAttempts(t *testing.T) {
	s := testServerWithRedis(t, Config{})
	ctx := context.Background()
	// Setup ativo (necessário para o Get passar antes do Incr).
	if err := s.rdb.Set(ctx, "mfa_setup:42", "JBSWY3DPEHPK3PXP", 0).Err(); err != nil {
		t.Fatalf("set mfa_setup: %v", err)
	}
	// Simula 5 tentativas anteriores.
	if err := s.rdb.Set(ctx, "api-go:mfa_enable_attempts:42", "5", 0).Err(); err != nil {
		t.Fatalf("set attempt counter: %v", err)
	}
	w := httptest.NewRecorder()
	r := reqAs(httptest.NewRequest("POST", "/auth/mfa/enable",
		strings.NewReader(`{"code":"123456"}`)), 42)
	s.handleMFAEnable(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("6ª tentativa deve retornar 429, veio %d", w.Code)
	}
	// Ambas as chaves devem ser apagadas (força o usuário a reiniciar o setup).
	if n, err := s.rdb.Exists(ctx, "mfa_setup:42").Result(); err != nil {
		t.Fatalf("redis exists mfa_setup: %v", err)
	} else if n != 0 {
		t.Error("mfa_setup deve ser apagado após lockout")
	}
	if n, err := s.rdb.Exists(ctx, "api-go:mfa_enable_attempts:42").Result(); err != nil {
		t.Fatalf("redis exists mfa_enable_attempts: %v", err)
	} else if n != 0 {
		t.Error("api-go:mfa_enable_attempts deve ser apagado após lockout")
	}
}

// TestHandleMFAEnableRedisDown garante que handleMFAEnable não retorna 200 quando o
// Redis está indisponível — a falha no Get do mfa_setup é tratada como setup expirado
// (fail-safe: o usuário não consegue ativar MFA em estado desconhecido).
func TestHandleMFAEnableRedisDown(t *testing.T) {
	mr := miniredis.RunT(t)
	s := testServer(Config{})
	s.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = s.rdb.Close() })

	mr.SetError("ERR simulated Redis failure")

	w := httptest.NewRecorder()
	r := reqAs(httptest.NewRequest("POST", "/auth/mfa/enable",
		strings.NewReader(`{"code":"123456"}`)), 42)
	s.handleMFAEnable(w, r)
	if w.Code == http.StatusOK {
		t.Fatal("Redis indisponível não deve permitir ativação de MFA (esperava != 200)")
	}
}
