package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── quizKeyAuthorize: só existência/revogação, sem banco ────────────────────
//
// Desde a correção de corrida da cota (spec 2026-09-22-extensao-quiz-jev),
// quizKeyAuthorize NÃO decide mais cota — só se a chave existe e não está
// revogada. A decisão de cota virou reserva atômica (quizKeyReserve, seção
// abaixo) cobrada em quiz.go no instante em que a questão já foi validada,
// não mais no guard. Ver o comentário grande em quizKeyAuthorize
// (quiz_keys.go) pro porquê completo.
//
// Estas são as únicas partes do fluxo de chaves testáveis neste harness (ver
// nota em handlers_social_test.go: s.db == nil, sem Postgres real disponível
// para os testes Go deste repo). Os casos que dependem de consulta/escrita
// real no banco (lookupQuizKey achar uma chave de verdade, reserveQuizKeyUsage
// gravar o contador) estão documentados como pendência no report da spec.

func TestQuizKeyAuthorizeChaveRevogadaBarra(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	revokedAt := now.Add(-time.Hour)
	k := &quizAccessKey{DailyLimit: 100, UsageDate: now, UsageCount: 0, RevokedAt: &revokedAt}
	err := quizKeyAuthorize(k)
	if err == nil {
		t.Fatal("esperava 401 para chave revogada, passou")
	}
	if err.Status != http.StatusUnauthorized || err.Code != "INVALID_KEY" {
		t.Fatalf("status/código inesperados: %d %s", err.Status, err.Code)
	}
}

func TestQuizKeyAuthorizeChaveInexistenteBarra(t *testing.T) {
	err := quizKeyAuthorize(nil) // nil = lookupQuizKey não achou a chave
	if err == nil {
		t.Fatal("esperava 401 para chave inexistente, passou")
	}
	if err.Status != http.StatusUnauthorized || err.Code != "INVALID_KEY" {
		t.Fatalf("status/código inesperados: %d %s", err.Status, err.Code)
	}
}

// Chave dentro da cota (ou até no limite exato) não é mais barrada AQUI — o
// guard só autentica. quizKeyAuthorize passa mesmo com a cota estourada;
// quem barra por cota agora é quizKeyReserve, mais adiante no fluxo.
func TestQuizKeyAuthorizePassaMesmoComCotaEstourada(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	k := &quizAccessKey{DailyLimit: 100, UsageDate: now, UsageCount: 100}
	if err := quizKeyAuthorize(k); err != nil {
		t.Fatalf("guard não deveria mais barrar por cota, veio erro: %v", err)
	}
}

// A mensagem de chave inexistente e a de chave revogada precisam ser
// IDÊNTICAS — senão a resposta vira um oráculo que deixa alguém descobrir se
// acertou o formato do hash mesmo sem ter uma chave válida.
func TestQuizKeyAuthorizeMensagemNaoRevelaFormatoCerto(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	revokedAt := now.Add(-time.Hour)
	revogada := quizKeyAuthorize(&quizAccessKey{DailyLimit: 1, RevokedAt: &revokedAt})
	inexistente := quizKeyAuthorize(nil)
	if revogada.Message != inexistente.Message || revogada.Code != inexistente.Code {
		t.Fatalf("mensagens/códigos deveriam ser idênticos: %q/%q vs %q/%q",
			revogada.Message, revogada.Code, inexistente.Message, inexistente.Code)
	}
}

// ── quizKeyEffectiveUsage: reset diário, sem banco ──────────────────────────

func TestQuizKeyEffectiveUsageDiaAnteriorNaoConta(t *testing.T) {
	hoje := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	ontem := hoje.AddDate(0, 0, -1)
	k := &quizAccessKey{DailyLimit: 10, UsageDate: ontem, UsageCount: 10} // "estourado" ontem
	if got := quizKeyEffectiveUsage(k, hoje); got != 0 {
		t.Fatalf("uso de ontem não deveria contar pro limite de hoje: got=%d, esperado 0", got)
	}
	// Consequência direta: com o contador de ontem, hoje reserva de novo.
	if _, granted := quizKeyReserve(k, hoje); !granted {
		t.Fatal("esperava conceder (contador é de ontem), negou")
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

// ── quizKeyReserve: a decisão de cota em si (pura) ──────────────────────────
//
// quizKeyReserve espelha, em Go puro, exatamente o que o UPDATE atômico de
// ReserveQuizKeyUsage decide em SQL (db/query/quiz_keys.sql) — ver o
// comentário em reserveQuizKeyUsage (quiz_keys.go) pro porquê do desenho.

func TestQuizKeyReserveDentroDoLimiteConcede(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	k := &quizAccessKey{DailyLimit: 100, UsageDate: now, UsageCount: 99}
	next, granted := quizKeyReserve(k, now)
	if !granted {
		t.Fatal("esperava conceder (99 de 100), negou")
	}
	if next != 100 {
		t.Fatalf("próximo contador=%d, esperado 100", next)
	}
}

func TestQuizKeyReserveNoLimiteExatoNega(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	k := &quizAccessKey{DailyLimit: 100, UsageDate: now, UsageCount: 100}
	if _, granted := quizKeyReserve(k, now); granted {
		t.Fatal("esperava negar no limite exato (100 de 100), concedeu")
	}
}

func TestQuizKeyReserveDiaAnteriorReiniciaEmUm(t *testing.T) {
	hoje := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	ontem := hoje.AddDate(0, 0, -1)
	k := &quizAccessKey{DailyLimit: 10, UsageDate: ontem, UsageCount: 10} // "estourado" ontem
	next, granted := quizKeyReserve(k, hoje)
	if !granted || next != 1 {
		t.Fatalf("esperava conceder e reiniciar em 1 (contador é de ontem), veio granted=%v next=%d", granted, next)
	}
}

// TestQuizKeyReserveConcorrenciaSoUmaPassaNoLimite é a prova pedida pela
// revisão de segurança: com a cota no limite, requisições concorrentes pela
// mesma chave não podem estourar em bloco.
//
// O QUE ESTE TESTE PROVA: quizKeyReserve (a decisão) é correta quando cada
// chamada é serializada (lock → ler estado → decidir → escrever estado →
// unlock) — exatamente o grau de atomicidade que um ÚNICO UPDATE...WHERE
// Postgres garante por linha, via lock de linha durante a instrução (ver
// reserveQuizKeyUsage e db/query/quiz_keys.sql: um round-trip só, sem
// SELECT-decide-UPDATE em passos separados). O mutex abaixo simula essa
// serialização; disparamos muito mais goroutines concorrentes do que a cota
// permite e verificamos que NUNCA mais que o limite é concedido, nem o
// contador final passa do limite.
//
// O QUE ESTE TESTE NÃO PROVA: que reserveQuizKeyUsage/o SQL/o driver pgx
// realmente se comportam assim contra um Postgres de verdade — isso exigiria
// um teste de integração com banco real, que este repo não tem pra Go (ver
// nota em handlers_social_test.go: sem testcontainers/TEST_DATABASE_URL
// pros testes Go deste harness). Documentado como lacuna no report da spec
// 2026-09-22-extensao-quiz-jev/task-chaves-fix-report.md — a garantia real
// de atomicidade Postgres (lock de linha durante um único UPDATE) é
// comportamento padrão e bem estabelecido do banco, não algo específico
// deste código que precisasse ser reprovado aqui.
func TestQuizKeyReserveConcorrenciaSoUmaPassaNoLimite(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	const dailyLimit = 5
	const attempts = 200 // bem mais tentativas concorrentes do que a cota permite

	k := &quizAccessKey{DailyLimit: dailyLimit, UsageDate: now, UsageCount: 0}
	var mu sync.Mutex
	var grantedCount int64

	var wg sync.WaitGroup
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			defer wg.Done()
			mu.Lock()
			next, granted := quizKeyReserve(k, now)
			if granted {
				k.UsageCount = next
			}
			mu.Unlock()
			if granted {
				atomic.AddInt64(&grantedCount, 1)
			}
		}()
	}
	wg.Wait()

	if grantedCount != dailyLimit {
		t.Fatalf("reservas concedidas=%d, esperado exatamente %d (o limite) de %d tentativas concorrentes — cota estourou ou ficou aquém",
			grantedCount, dailyLimit, attempts)
	}
	if k.UsageCount != dailyLimit {
		t.Fatalf("usage_count final=%d, esperado exatamente %d", k.UsageCount, dailyLimit)
	}
}

// ── quizQuotaExceededErr: mensagem em horário de Brasília ───────────────────

func TestQuizQuotaExceededErrMensagemEmHorarioDeBrasilia(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	err := quizQuotaExceededErr(now)
	if err.Status != http.StatusTooManyRequests || err.Code != "QUOTA_EXCEEDED" {
		t.Fatalf("status/código inesperados: %d %s", err.Status, err.Code)
	}
	if err.Message == "" {
		t.Fatal("mensagem de 429 não pode vir vazia — precisa dizer quando reseta")
	}
	// O reset em si continua sendo meia-noite UTC (quizNextResetAt, sem
	// mudança) — só a mensagem passa a deixar explícito o horário de
	// Brasília (21:00, sem horário de verão desde 2019), pro público
	// brasileiro da extensão não precisar converter fuso de cabeça.
	if !strings.Contains(err.Message, "Brasília") {
		t.Fatalf("mensagem devia deixar explícito o horário de Brasília: %q", err.Message)
	}
	if !strings.Contains(err.Message, "21:00") {
		t.Fatalf("meia-noite UTC = 21:00 em Brasília, devia aparecer na mensagem: %q", err.Message)
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
