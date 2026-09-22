package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/santos-tech/auth/db"
)

func TestQuizErrStatus(t *testing.T) {
	casos := []struct {
		err   error
		code  string
		http_ int
	}{
		{errQuizUnparseable, "UNPARSEABLE", http.StatusUnprocessableEntity},
		{errQuizTimeout, "UPSTREAM_TIMEOUT", http.StatusGatewayTimeout},
		{errQuizUpstream, "UPSTREAM_FAILED", http.StatusBadGateway},
		{errAPIRouterNoActiveKeys, "NO_ACTIVE_KEYS", http.StatusServiceUnavailable},
		{errQuizImagemMimeInvalido, "INVALID_IMAGE", http.StatusBadRequest},
		{errQuizImagemBase64Invalido, "INVALID_IMAGE", http.StatusBadRequest},
		{errQuizImagemGrandeDemais, "INVALID_IMAGE", http.StatusBadRequest},
		{errors.New("qualquer outra"), "UPSTREAM_FAILED", http.StatusBadGateway},
	}
	for _, c := range casos {
		ae := quizErr(c.err)
		if ae.Code != c.code || ae.Status != c.http_ {
			t.Errorf("quizErr(%v) = %s/%d, queria %s/%d", c.err, ae.Code, ae.Status, c.code, c.http_)
		}
	}
	// As três causas de imagem inválida têm o mesmo código, mas mensagens
	// diferentes — achado da revisão: base64 malformado não pode soar como
	// "mime não suportado".
	msgMime := quizErr(errQuizImagemMimeInvalido).Message
	msgB64 := quizErr(errQuizImagemBase64Invalido).Message
	msgTamanho := quizErr(errQuizImagemGrandeDemais).Message
	if msgMime == msgB64 || msgMime == msgTamanho || msgB64 == msgTamanho {
		t.Errorf("mensagens de imagem inválida deveriam ser distintas: mime=%q base64=%q tamanho=%q", msgMime, msgB64, msgTamanho)
	}
	if !strings.Contains(strings.ToLower(msgB64), "base64") {
		t.Errorf("mensagem de base64 inválido não menciona base64: %q", msgB64)
	}
}

// ── corpo grande via HTTP real (handleQuizAnswer) ───────────────────────────
//
// Achado da revisão (Critical): handlers_quiz.go setava um MaxBytesReader de
// quizMaxBodyLen (8MB) e em seguida chamava decodeJSON — que aninha OUTRO
// MaxBytesReader de 1MB fixo (maxJSONBody, server.go) por cima. O menor
// prevalece: o teto efetivo virava ~1MB, e nenhuma imagem realista passava.
// Os testes de quiz_test.go chamam answerQuiz direto (sem o handler HTTP),
// por isso não pegaram o bug — o MaxBytesReader nunca entrou no caminho.
// Este teste exercita handleQuizAnswer de verdade via httptest.

// quizFakeRow satisfaz pgx.Row: Scan sempre devolve o erro configurado. Não
// importa o conteúdo — só precisamos provar que a chamada ao banco
// ACONTECEU, o que só é possível se o corpo grande passou pelo decode.
type quizFakeRow struct{ err error }

func (r quizFakeRow) Scan(dest ...any) error { return r.err }

// quizFakeDBTX satisfaz db.DBTX o mínimo pra GetAPIRouterProvider rodar —
// só QueryRow é exercitado pelo caminho que este teste percorre.
type quizFakeDBTX struct{}

func (quizFakeDBTX) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("quizFakeDBTX: Exec não implementado")
}

func (quizFakeDBTX) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, errors.New("quizFakeDBTX: Query não implementado")
}

func (quizFakeDBTX) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return quizFakeRow{err: errors.New("provider não encontrado (fake de teste)")}
}

// TestHandleQuizAnswerAceitaCorpoAcimaDe1MB manda ~2MB (acima do teto antigo
// de 1MB, dentro do novo de 8MB) direto no handler real e confere que a
// resposta NÃO é 400 INVALID_BODY por tamanho. O fake de banco devolve erro
// de propósito — chegar até ele (503 NOT_CONFIGURED) É a prova de que o
// corpo passou pelo decode; se o teto errado ainda estivesse em vigor, a
// resposta seria 400 INVALID_BODY antes de qualquer tentativa de banco.
func TestHandleQuizAnswerAceitaCorpoAcimaDe1MB(t *testing.T) {
	s := testServer(Config{QuizJevProviderID: 1})
	s.vault = &Vault{} // só precisa ser não-nil pra passar de apiRouterNotConfigured
	s.q = db.New(quizFakeDBTX{})

	// ~2MB de padding dentro de um raw válido — acima do teto antigo (1MB)
	// e dentro do novo (quizMaxBodyLen, 8MB).
	padding := strings.Repeat("x", 2<<20)
	payload, err := json.Marshal(quizRequest{Raw: "Pergunta?\nA) " + padding + "\nB) y"})
	if err != nil {
		t.Fatalf("marshal do payload de teste: %v", err)
	}
	if len(payload) <= 1<<20 {
		t.Fatalf("payload de teste tem %d bytes, precisa ser maior que 1MB pra provar o bug", len(payload))
	}

	r := httptest.NewRequest(http.MethodPost, "/quiz/answer", strings.NewReader(string(payload)))
	w := httptest.NewRecorder()
	s.handleQuizAnswer(w, r)

	var respBody map[string]string
	if err := json.NewDecoder(w.Result().Body).Decode(&respBody); err != nil {
		t.Fatalf("resposta não é JSON: %v", err)
	}
	if respBody["code"] == "INVALID_BODY" {
		t.Fatalf("corpo de %d bytes (>1MB) rejeitado como INVALID_BODY (status=%d) — quizMaxBodyLen (%d) não está valendo em runtime",
			len(payload), w.Code, quizMaxBodyLen)
	}
	if w.Code != http.StatusServiceUnavailable || respBody["code"] != "NOT_CONFIGURED" {
		t.Errorf("code=%q status=%d, queria NOT_CONFIGURED/503 (erro do fake de banco — prova que passou do decode, %s)",
			respBody["code"], w.Code, fmt.Sprintf("payload=%dB", len(payload)))
	}
}

func TestIsNativeClientAceitaExtensaoFirefox(t *testing.T) {
	// Fetch de extensão Firefox SEMPRE manda Origin: moz-extension://<uuid>.
	// Sem isto o login devolve só cookies httpOnly SameSite=Lax, que a
	// extensão não consegue ler nem reenviar: logada e sem token, pra sempre.
	r := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	r.Header.Set("Origin", "moz-extension://8f3a1c2e-0000-4000-8000-abcdefabcdef")
	if !isNativeClient(r) {
		t.Error("extensão Firefox precisa receber os tokens no corpo")
	}
}

func TestIsNativeClientRecusaOrigemWeb(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	r.Header.Set("Origin", "https://santos-tech.com")
	if isNativeClient(r) {
		t.Error("origem web continua no fluxo de cookie")
	}
}

func TestIsNativeClientSemOrigem(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	if !isNativeClient(r) {
		t.Error("cliente nativo (sem Origin) continua valendo")
	}
}
