package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

func newWebhookFixture(t *testing.T) (*WebhookHandler, *asynq.Inspector) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	opt := asynq.RedisClientOpt{Addr: mr.Addr()}
	client := asynq.NewClient(opt)
	t.Cleanup(func() { _ = client.Close() })
	insp := asynq.NewInspector(opt)
	t.Cleanup(func() { _ = insp.Close() })
	h := &WebhookHandler{
		cfg:    Config{WebhookSecret: "s", GroupJID: "120@g.us", MaxFixAttempts: 2},
		client: client,
		store:  &Store{rdb: redis.NewClient(&redis.Options{Addr: mr.Addr()})},
		cool:   nil, // sem rede no teste
	}
	return h, insp
}

func post(h *WebhookHandler, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/webhooks/coolify?token="+token, strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func pending(t *testing.T, insp *asynq.Inspector) int {
	t.Helper()
	tasks, err := insp.ListPendingTasks("default")
	if err != nil {
		// Nenhuma task enfileirada ainda → a fila nem existe no Redis.
		if strings.Contains(err.Error(), "queue not found") {
			return 0
		}
		t.Fatal(err)
	}
	return len(tasks)
}

func TestWebhookRejectsBadToken(t *testing.T) {
	h, _ := newWebhookFixture(t)
	if w := post(h, "x", `{}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("esperava 401, veio %d", w.Code)
	}
}

func TestWebhookEnqueuesOnFailure(t *testing.T) {
	h, insp := newWebhookFixture(t)
	w := post(h, "s", `{"status":"failed","application_name":"bot-go","deployment_uuid":"d1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d", w.Code)
	}
	if n := pending(t, insp); n != 1 {
		t.Fatalf("esperava 1 task incident:create, veio %d", n)
	}
}

func TestWebhookDeduplicatesDeployment(t *testing.T) {
	h, insp := newWebhookFixture(t)
	body := `{"status":"failed","application_name":"bot-go","deployment_uuid":"dup"}`
	_ = post(h, "s", body)
	_ = post(h, "s", body) // duplicado: não deve enfileirar de novo
	if n := pending(t, insp); n != 1 {
		t.Fatalf("dedupe falhou: esperava 1, veio %d", n)
	}
}

func TestWebhookSuccessWithoutIncidentDoesNothing(t *testing.T) {
	h, insp := newWebhookFixture(t)
	w := post(h, "s", `{"status":"success","application_name":"bot-go","deployment_uuid":"d2"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d", w.Code)
	}
	if n := pending(t, insp); n != 0 {
		t.Fatalf("deploy normal não deve enfileirar: veio %d", n)
	}
}
