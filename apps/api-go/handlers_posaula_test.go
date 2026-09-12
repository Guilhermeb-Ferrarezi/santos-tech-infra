package main

// Testes dos handlers do Pós-aula — só os caminhos que retornam ANTES de
// tocar o banco (padrão do repo, ver handlers_portal_diario_test.go), o
// guard (via usuário em cache no miniredis) e o cliente do agent-go contra
// um servidor HTTP falso.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func answerReq(taskID, body string, userID int64) *http.Request {
	r := httptest.NewRequest("POST", "/portal/me/tasks/"+taskID+"/answer", strings.NewReader(body))
	r.SetPathValue("taskId", taskID)
	return reqAs(r, userID)
}

func TestPortalAnswerTaskValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})
	cases := []struct {
		name   string
		taskID string
		body   string
	}{
		{"id inválido", "x", `{"selectedOption":0}`},
		{"corpo inválido", "1", "xxx"},
		{"campo desconhecido", "1", `{"selectedOption":0,"gabarito":"0"}`},
		{"nenhum campo", "1", `{}`},
		{"texto vazio e sem opção", "1", `{"answerText":"   "}`},
		{"texto acima de 8.000", "1", `{"answerText":"` + strings.Repeat("a", posaulaAnswerTextMax+1) + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.handlePortalAnswerTask(w, answerReq(tc.taskID, tc.body, 1))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code=%d want %d body=%s", w.Code, http.StatusBadRequest, w.Body.String())
			}
		})
	}
}

func TestPortalMyTasksValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})
	for name, path := range map[string]string{
		"status inválido":  "/portal/me/tasks?status=todas",
		"classId inválido": "/portal/me/tasks?status=pending&classId=abc",
		"limit inválido":   "/portal/me/tasks?limit=abc",
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.handlePortalMyTasks(w, reqAs(httptest.NewRequest("GET", path, nil), 1))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code=%d want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestPortalPosaulaRoutesRequireAuthBeforeDB(t *testing.T) {
	s := testServer(Config{})
	for _, tc := range []struct {
		name   string
		h      http.HandlerFunc
		method string
		path   string
	}{
		{"minhas práticas", s.authGuard(s.handlePortalMyTasks), "GET", "/portal/me/tasks"},
		{"responder", s.authGuard(s.handlePortalAnswerTask), "POST", "/portal/me/tasks/1/answer"},
		{"práticas da aula", s.portalDiario("read", s.handlePortalSessionTasks), "GET", "/portal/sessions/1/tasks"},
		{"regenerar", s.portalDiario("write", s.handlePortalRegenerateTasks), "POST", "/portal/sessions/1/tasks/regenerate"},
		{"excluir", s.portalDiario("write", s.handlePortalDeleteTask), "DELETE", "/portal/tasks/1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.h(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("code=%d want %d", w.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestPortalPosaulaBadIDsBeforeDB(t *testing.T) {
	s := testServer(Config{})
	// Qualquer coisa que não seja inteiro positivo é 400, antes de tocar o banco.
	for _, tc := range []struct {
		name string
		fn   http.HandlerFunc
		key  string
		val  string
		path string
	}{
		{"práticas da aula", s.handlePortalSessionTasks, "sessionId", "x", "/portal/sessions/x/tasks"},
		{"regenerar", s.handlePortalRegenerateTasks, "sessionId", "0", "/portal/sessions/0/tasks/regenerate"},
		{"excluir", s.handlePortalDeleteTask, "taskId", "-1", "/portal/tasks/-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.path, nil)
			r.SetPathValue(tc.key, tc.val)
			w := httptest.NewRecorder()
			tc.fn(w, reqAs(r, 1))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code=%d want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

// Guard das rotas do professor: aluno não entra (403), professor e admin
// entram, cargo personalizado só com portal_diario. É o MESMO guard do
// diário — o teste existe pra garantir que as rotas novas usam ele.
func TestPortalPosaulaGuard(t *testing.T) {
	cfg := Config{JWTSecret: "s-access", JWTRefreshSecret: "s-refresh"}
	s := testServerWithRedis(t, cfg)
	cargoComDiario := "33333333-3333-3333-3333-333333333333"
	b, _ := json.Marshal(map[string][]string{"portal_diario": {"read"}})
	if err := s.rdb.Set(context.Background(), "api-go:auth:custom_role:"+cargoComDiario, b, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	professor := seedCachedUser(t, s, User{ID: 31, Email: "prof@santos-tech.com", Name: "Prof", Role: RoleTeacher})
	aluno := seedCachedUser(t, s, User{ID: 32, Email: "aluno@x.com", Name: "Aluno", Role: RoleStudent})
	admin := seedCachedUser(t, s, User{ID: 33, Email: "adm@santos-tech.com", Name: "Adm", Role: RoleAdmin})
	soLeitura := seedCachedUser(t, s, User{ID: 34, Email: "c@santos-tech.com", Role: RoleCustom, CustomRoleID: &cargoComDiario})

	stub := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	for _, tc := range []struct {
		name   string
		action string
		cookie *http.Cookie
		want   int
	}{
		{"aluno não vê práticas da aula", "read", aluno, http.StatusForbidden},
		{"aluno não exclui", "write", aluno, http.StatusForbidden},
		{"professor vê", "read", professor, http.StatusNoContent},
		{"professor exclui/regenera", "write", professor, http.StatusNoContent},
		{"admin exclui/regenera", "write", admin, http.StatusNoContent},
		{"cargo só leitura vê", "read", soLeitura, http.StatusNoContent},
		{"cargo só leitura não exclui", "write", soLeitura, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/portal/sessions/1/tasks", nil)
			r.AddCookie(tc.cookie)
			w := httptest.NewRecorder()
			s.portalDiario(tc.action, stub)(w, r)
			if w.Code != tc.want {
				t.Fatalf("code=%d want %d body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// Sem asynq (s.queue == nil) o enqueue avisa em vez de tocar o banco.
func TestEnqueuePosaulaSemFila(t *testing.T) {
	s := testServer(Config{})
	if err := s.enqueuePosaulaGerar(context.Background(), 1, time.Now()); !errors.Is(err, errFilaIndisponivel) {
		t.Errorf("gerar sem fila: err=%v want errFilaIndisponivel", err)
	}
	if err := s.enqueuePosaulaCorrigir(context.Background(), 1); !errors.Is(err, errFilaIndisponivel) {
		t.Errorf("corrigir sem fila: err=%v want errFilaIndisponivel", err)
	}
}

// ── agent_client.go ──────────────────────────────────────────────────────────

func TestClaudeRawSemSecret(t *testing.T) {
	s := testServer(Config{AgentURL: "http://127.0.0.1:1"})
	_, err := s.claudeRaw(context.Background(), "oi", "sonnet")
	if err == nil || !strings.Contains(err.Error(), "AGENT_INTERNAL_SECRET") {
		t.Fatalf("sem secret deveria falhar com erro claro, veio %v", err)
	}
	if errors.As(err, new(agentTransientError)) {
		t.Error("falta de secret não é erro transitório — asynq não deve retentar")
	}
}

func TestClaudeRawChamaAgentGo(t *testing.T) {
	var calls atomic.Int32
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		if r.URL.Path != "/claude/generate" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"text":"{\"ok\":true}"}`))
	}))
	defer srv.Close()
	s := testServer(Config{AgentURL: srv.URL, AgentInternalSecret: "segredo"})
	text, err := s.claudeRaw(context.Background(), "brief de teste", "sonnet")
	if err != nil {
		t.Fatal(err)
	}
	if text != `{"ok":true}` {
		t.Errorf("text=%q", text)
	}
	if gotAuth != "Bearer segredo" {
		t.Errorf("Authorization=%q", gotAuth)
	}
	var req claudeGenerateRequest
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil || req.Task != "raw" || req.Brief != "brief de teste" || req.Model != "sonnet" {
		t.Errorf("body=%s err=%v", gotBody, err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls=%d want 1", calls.Load())
	}
}

// 5xx → uma retentativa; 401 → desiste na hora (não é transitório).
func TestClaudeRawRetentativa(t *testing.T) {
	antes := claudeRawRetryDelay
	claudeRawRetryDelay = 0
	t.Cleanup(func() { claudeRawRetryDelay = antes })
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"text":"segunda vez"}`))
	}))
	defer srv.Close()
	s := testServer(Config{AgentURL: srv.URL, AgentInternalSecret: "segredo"})
	text, err := s.claudeRaw(context.Background(), "x", "sonnet")
	if err != nil || text != "segunda vez" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls=%d want 2", calls.Load())
	}

	var calls401 atomic.Int32
	srv401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls401.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv401.Close()
	s2 := testServer(Config{AgentURL: srv401.URL, AgentInternalSecret: "errado"})
	_, err = s2.claudeRaw(context.Background(), "x", "sonnet")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("401 deveria virar erro com o status, veio %v", err)
	}
	if errors.As(err, new(agentTransientError)) {
		t.Error("401 não é transitório")
	}
	if calls401.Load() != 1 {
		t.Errorf("401 não deveria retentar: calls=%d", calls401.Load())
	}

	// 5xx nas duas tentativas → erro transitório (asynq retenta mais tarde)
	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv500.Close()
	s3 := testServer(Config{AgentURL: srv500.URL, AgentInternalSecret: "segredo"})
	_, err = s3.claudeRaw(context.Background(), "x", "sonnet")
	if !errors.As(err, new(agentTransientError)) {
		t.Errorf("5xx persistente deveria ser transitório, veio %v", err)
	}
}

// 429 (balde de 10/min do agent-go cheio) e 503 são transitórios: uma
// retentativa interna e, persistindo, erro transitório pro asynq.
func TestClaudeRaw429ETransitorio(t *testing.T) {
	antes := claudeRawRetryDelay
	claudeRawRetryDelay = 0
	t.Cleanup(func() { claudeRawRetryDelay = antes })

	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
				return
			}
			_, _ = w.Write([]byte(`{"text":"passou"}`))
		}))
		s := testServer(Config{AgentURL: srv.URL, AgentInternalSecret: "segredo"})
		text, err := s.claudeRaw(context.Background(), "x", "sonnet")
		srv.Close()
		if err != nil || text != "passou" {
			t.Errorf("%d: text=%q err=%v — deveria retentar e passar", status, text, err)
		}
		if calls.Load() != 2 {
			t.Errorf("%d: calls=%d want 2", status, calls.Load())
		}
	}

	var calls atomic.Int32
	srv429 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv429.Close()
	s := testServer(Config{AgentURL: srv429.URL, AgentInternalSecret: "segredo"})
	_, err := s.claudeRaw(context.Background(), "x", "sonnet")
	if !errors.As(err, new(agentTransientError)) {
		t.Errorf("429 persistente deveria ser transitório (asynq retenta com backoff), veio %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("429 persistente: calls=%d want 2", calls.Load())
	}
}

// Retry-After em segundos é respeitado antes da retentativa interna.
func TestClaudeRawRespeitaRetryAfter(t *testing.T) {
	antes := claudeRawRetryDelay
	claudeRawRetryDelay = 0
	t.Cleanup(func() { claudeRawRetryDelay = antes })
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer srv.Close()
	s := testServer(Config{AgentURL: srv.URL, AgentInternalSecret: "segredo"})
	inicio := time.Now()
	if _, err := s.claudeRaw(context.Background(), "x", "sonnet"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(inicio); d < time.Second {
		t.Errorf("deveria ter esperado o Retry-After de 1s antes de retentar; levou %s", d)
	}
}

func TestRetryAfterEspera(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"":                              0,
		"abc":                           0,
		"0":                             0,
		"-3":                            0,
		"7":                             7 * time.Second,
		" 12 ":                          12 * time.Second,
		"60":                            60 * time.Second,
		"90":                            claudeRawRetryAfterMax,
		"Wed, 21 Oct 2026 07:28:00 GMT": 0, // data HTTP: cai na pausa padrão
	} {
		if got := retryAfterEspera(raw); got != want {
			t.Errorf("Retry-After %q → %s want %s", raw, got, want)
		}
	}
}
