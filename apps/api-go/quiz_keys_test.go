package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ── quizKeyAuthorize / quizKeyEffectiveUsage: decisão pura, sem banco ───────
//
// Estas são as únicas partes do fluxo de chaves testáveis neste harness (ver
// nota em handlers_social_test.go: s.db == nil, sem Postgres real disponível
// para os testes Go deste repo). Os casos que dependem de consulta/escrita
// real no banco (lookupQuizKey achar uma chave de verdade, incrementQuizKeyUsage
// gravar o contador) estão documentados como pendência no report da spec
// 2026-09-22-extensao-quiz-jev/task-chaves-report.md.

func TestQuizKeyAuthorizeDentroDoLimitePassa(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	k := &quizAccessKey{DailyLimit: 100, UsageDate: now, UsageCount: 99}
	if err := quizKeyAuthorize(k, now); err != nil {
		t.Fatalf("esperava passar (99 de 100), veio erro: %v", err)
	}
}

func TestQuizKeyAuthorizeNoLimiteExatoBarra(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	k := &quizAccessKey{DailyLimit: 100, UsageDate: now, UsageCount: 100}
	err := quizKeyAuthorize(k, now)
	if err == nil {
		t.Fatal("esperava 429 no limite exato (100 de 100), passou")
	}
	if err.Status != http.StatusTooManyRequests || err.Code != "QUOTA_EXCEEDED" {
		t.Fatalf("status/código inesperados: %d %s", err.Status, err.Code)
	}
	if err.Message == "" {
		t.Error("mensagem de 429 não pode vir vazia — precisa dizer quando reseta")
	}
}

func TestQuizKeyAuthorizeChaveRevogadaBarra(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	revokedAt := now.Add(-time.Hour)
	k := &quizAccessKey{DailyLimit: 100, UsageDate: now, UsageCount: 0, RevokedAt: &revokedAt}
	err := quizKeyAuthorize(k, now)
	if err == nil {
		t.Fatal("esperava 401 para chave revogada, passou")
	}
	if err.Status != http.StatusUnauthorized || err.Code != "INVALID_KEY" {
		t.Fatalf("status/código inesperados: %d %s", err.Status, err.Code)
	}
}

func TestQuizKeyAuthorizeChaveInexistenteBarra(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	err := quizKeyAuthorize(nil, now) // nil = lookupQuizKey não achou a chave
	if err == nil {
		t.Fatal("esperava 401 para chave inexistente, passou")
	}
	if err.Status != http.StatusUnauthorized || err.Code != "INVALID_KEY" {
		t.Fatalf("status/código inesperados: %d %s", err.Status, err.Code)
	}
}

// A mensagem de chave inexistente e a de chave revogada precisam ser
// IDÊNTICAS — senão a resposta vira um oráculo que deixa alguém descobrir se
// acertou o formato do hash mesmo sem ter uma chave válida.
func TestQuizKeyAuthorizeMensagemNaoRevelaFormatoCerto(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	revokedAt := now.Add(-time.Hour)
	revogada := quizKeyAuthorize(&quizAccessKey{DailyLimit: 1, RevokedAt: &revokedAt}, now)
	inexistente := quizKeyAuthorize(nil, now)
	if revogada.Message != inexistente.Message || revogada.Code != inexistente.Code {
		t.Fatalf("mensagens/códigos deveriam ser idênticos: %q/%q vs %q/%q",
			revogada.Message, revogada.Code, inexistente.Message, inexistente.Code)
	}
}

func TestQuizKeyEffectiveUsageDiaAnteriorNaoConta(t *testing.T) {
	hoje := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	ontem := hoje.AddDate(0, 0, -1)
	k := &quizAccessKey{DailyLimit: 10, UsageDate: ontem, UsageCount: 10} // "estourado" ontem
	if got := quizKeyEffectiveUsage(k, hoje); got != 0 {
		t.Fatalf("uso de ontem não deveria contar pro limite de hoje: got=%d, esperado 0", got)
	}
	// Consequência direta: com o contador de ontem, hoje passa de novo.
	if err := quizKeyAuthorize(k, hoje); err != nil {
		t.Fatalf("esperava passar (contador é de ontem), veio erro: %v", err)
	}
}

func TestQuizKeyEffectiveUsageMesmoDiaConta(t *testing.T) {
	hoje := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	maisTardeHoje := time.Date(2026, 9, 22, 23, 59, 0, 0, time.UTC)
	k := &quizAccessKey{DailyLimit: 10, UsageDate: hoje, UsageCount: 5}
	if got := quizKeyEffectiveUsage(k, maisTardeHoje); got != 5 {
		t.Fatalf("uso do mesmo dia (UTC) deveria contar: got=%d, esperado 5", got)
	}
}

// ── quizAccessGuard: só os ramos que não tocam o banco (s.db == nil) ───────

// Sem header X-Quiz-Key e sem sessão → 401 como hoje (authGuard), sem tentar
// ler chave nenhuma — mesmo padrão de TestAdminGuardNoToken.
func TestQuizAccessGuardSemHeaderSemSessao(t *testing.T) {
	s := testServer(Config{})
	h := s.quizAccessGuard(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("POST", "/quiz/answer", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, esperado 401", w.Code)
	}
}

// Chave malformada (sem o prefixo qz_) é rejeitada ANTES de qualquer consulta
// ao banco — se tocasse s.db aqui (nil neste harness) o teste travaria com
// nil pointer em vez de devolver 401, então isto também prova que o guard não
// consulta o banco pra prefixo errado.
func TestQuizAccessGuardChaveMalformadaNaoTocaBanco(t *testing.T) {
	s := testServer(Config{})
	h := s.quizAccessGuard(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/quiz/answer", nil)
	r.Header.Set("X-Quiz-Key", "chave-sem-prefixo-certo")
	h(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, esperado 401", w.Code)
	}
	if got := w.Body.String(); got == "" {
		t.Fatal("corpo do erro vazio")
	}
}

// Sessão válida sem header de chave é o caminho de authGuard puro — este
// teste só confirma que, SEM sessão nenhuma, o guard devolve exatamente o
// mesmo 401 que authGuard devolveria sozinho (não regrediu o caminho de
// sessão). O caso "sessão válida → passa, sem tocar no contador" exige um
// token/JWT válido resolvido via s.db (nil aqui) — sem cobertura possível
// neste harness, ver report.
func TestQuizAccessGuardComparadoComAuthGuardPuro(t *testing.T) {
	s := testServer(Config{})
	hQuiz := s.quizAccessGuard(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	hAuth := s.authGuard(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	wQuiz := httptest.NewRecorder()
	hQuiz(wQuiz, httptest.NewRequest("POST", "/quiz/answer", nil))

	wAuth := httptest.NewRecorder()
	hAuth(wAuth, httptest.NewRequest("POST", "/quiz/answer", nil))

	if wQuiz.Code != wAuth.Code || wQuiz.Body.String() != wAuth.Body.String() {
		t.Fatalf("quizAccessGuard sem header deveria se comportar igual a authGuard: got=(%d,%q) authGuard=(%d,%q)",
			wQuiz.Code, wQuiz.Body.String(), wAuth.Code, wAuth.Body.String())
	}
}

func TestQuizKeyFromContextVazioSemChave(t *testing.T) {
	r := httptest.NewRequest("POST", "/quiz/answer", nil)
	if k := quizKeyFromContext(r.Context()); k != nil {
		t.Fatalf("esperava nil sem chave no contexto, veio %+v", k)
	}
}
