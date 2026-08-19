package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// deadlineRecorder é um ResponseWriter que registra o write deadline pedido pelo
// handler. http.NewResponseController usa a interface opcional SetWriteDeadline.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline    time.Time
	deadlineSet bool
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.deadline = t
	d.deadlineSet = true
	return nil
}

// TestStartSSE_RemoveWriteDeadline é a regressão do stream morto: o WriteTimeout do
// servidor (60s) é o prazo da resposta INTEIRA, então sem zerar o deadline o SSE de
// /pay/{token}/events era cortado aos 60s e o cliente nunca recebia o 'paid' de um
// PIX pago depois disso.
func TestStartSSE_RemoveWriteDeadline(t *testing.T) {
	w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	flusher, ok := startSSE(w)
	if !ok {
		t.Fatal("startSSE deveria funcionar com um ResponseRecorder")
	}
	if !w.deadlineSet {
		t.Fatal("o write deadline não foi tocado — o stream vai morrer no WriteTimeout")
	}
	if !w.deadline.IsZero() {
		t.Fatalf("o deadline deveria ser zerado (sem prazo), veio %v", w.deadline)
	}
	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type esperado text/event-stream, veio %q", got)
	}
	if got := w.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering deveria ser \"no\" para não haver buffering de proxy, veio %q", got)
	}
	if flusher == nil {
		t.Fatal("flusher nulo")
	}
}

// TestStartSSE_SemFlusher: um ResponseWriter que não sabe fazer flush não serve para
// SSE — o handler precisa detectar isso e responder erro.
func TestStartSSE_SemFlusher(t *testing.T) {
	if _, ok := startSSE(noFlushWriter{}); ok {
		t.Fatal("startSSE deveria falhar sem http.Flusher")
	}
}

type noFlushWriter struct{}

func (noFlushWriter) Header() http.Header         { return http.Header{} }
func (noFlushWriter) Write(b []byte) (int, error) { return len(b), nil }
func (noFlushWriter) WriteHeader(int)             {}

// TestSSEKeepAlive: o ping é um COMENTÁRIO SSE (linha iniciada por ':'), que o
// EventSource ignora — não pode virar um evento espúrio na tela do cliente.
func TestSSEKeepAlive(t *testing.T) {
	rec := httptest.NewRecorder()
	sseKeepAlive(rec, rec)
	body := rec.Body.String()
	if !strings.HasPrefix(body, ":") {
		t.Fatalf("keepalive precisa ser um comentário SSE, veio %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("keepalive precisa terminar em linha em branco, veio %q", body)
	}
	if strings.Contains(body, "event:") || strings.Contains(body, "data:") {
		t.Fatalf("keepalive não pode parecer um evento, veio %q", body)
	}
	if sseKeepAliveInterval >= 60*time.Second {
		t.Fatalf("o ping precisa ser bem menor que o WriteTimeout/idle dos proxies, veio %v", sseKeepAliveInterval)
	}
}
