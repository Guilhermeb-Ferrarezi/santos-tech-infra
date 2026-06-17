package golog

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureSlog redireciona o slog default p/ um buffer JSON e restaura no fim.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestRequestLogger_CapturaStatusEChamaNext(t *testing.T) {
	called := false
	h := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("oi"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if !called {
		t.Fatal("next não foi chamado")
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status=%d, quer %d", rec.Code, http.StatusTeapot)
	}
	if rec.Body.String() != "oi" {
		t.Fatalf("body=%q, quer %q", rec.Body.String(), "oi")
	}
}

func TestRequestLogger_GeraRequestID(t *testing.T) {
	h := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestIDFromContext(r.Context()) == "" {
			t.Error("request id ausente no context dentro do handler")
		}
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("X-Request-Id ausente na resposta")
	}
}

func TestRequestLogger_PreservaRequestIDDeEntrada(t *testing.T) {
	const rid = "rid-fixo-123"
	var seen string
	h := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("X-Request-Id", rid)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if seen != rid {
		t.Errorf("ctx request_id=%q, quer %q", seen, rid)
	}
	if got := rec.Header().Get("X-Request-Id"); got != rid {
		t.Errorf("header X-Request-Id=%q, quer %q", got, rid)
	}
}

func TestRequestLogger_RecuperaPanic(t *testing.T) {
	h := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil)) // não deve propagar
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, quer 500 após panic", rec.Code)
	}
}

func TestLogResponseWriter_PreservaFlusher(t *testing.T) {
	rec := httptest.NewRecorder() // ResponseRecorder implementa http.Flusher
	lw := &logResponseWriter{ResponseWriter: rec, status: http.StatusOK}
	var _ http.Flusher = lw // garante em compile-time que o wrapper é Flusher
	lw.Flush()              // não deve entrar em pânico
}

func TestLogRemoteIP_PrefereCloudflare(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Set("Cf-Connecting-Ip", "203.0.113.7")
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	if got := logRemoteIP(req); got != "203.0.113.7" {
		t.Errorf("ip=%q, quer 203.0.113.7 (CF-Connecting-IP)", got)
	}
}

// ── captura de payload (request + response) ──────────────────────────────────

func TestBody_HandlerRecebeBodyCompletoEReadigeSenha(t *testing.T) {
	buf := captureSlog(t)
	var got string
	h := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	body := `{"email":"aluno@x.com","password":"segredo123"}`
	req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got != body {
		t.Fatalf("handler recebeu body alterado: %q (quer %q)", got, body)
	}
	out := buf.String()
	if !strings.Contains(out, "aluno@x.com") {
		t.Error("campo não-sensível (email) deveria aparecer no log")
	}
	if strings.Contains(out, "segredo123") {
		t.Error("senha NÃO pode aparecer crua no log")
	}
	if !strings.Contains(out, "***") {
		t.Error("senha deveria estar redigida como ***")
	}
}

func TestBody_CapturaRespostaEReadige(t *testing.T) {
	buf := captureSlog(t)
	h := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"token":"jwt-abc-secreto","ok":true}`))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/me", nil))
	out := buf.String()
	if !strings.Contains(out, "resp_body") {
		t.Error("resp_body deveria estar presente")
	}
	if !strings.Contains(out, `\"ok\":true`) && !strings.Contains(out, `"ok":true`) {
		t.Error("conteúdo da resposta deveria aparecer")
	}
	if strings.Contains(out, "jwt-abc-secreto") {
		t.Error("token NÃO pode aparecer cru na resposta logada")
	}
}

func TestBody_Trunca(t *testing.T) {
	old := logBodyMaxBytes
	logBodyMaxBytes = 10
	defer func() { logBodyMaxBytes = old }()
	buf := captureSlog(t)
	h := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("POST", "/x", strings.NewReader(strings.Repeat("A", 100)))
	req.Header.Set("Content-Type", "text/plain")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !strings.Contains(buf.String(), "req_body_truncated") {
		t.Error("body grande deveria ser marcado como truncado")
	}
}

func TestBody_PulaMultipart(t *testing.T) {
	buf := captureSlog(t)
	h := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	raw := "------x\r\nbinario-da-imagem\r\n------x--"
	req := httptest.NewRequest("POST", "/auth/upload", strings.NewReader(raw))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=----x")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if strings.Contains(buf.String(), "binario-da-imagem") {
		t.Error("conteúdo multipart (upload) não deve ser logado cru")
	}
}

func TestBody_RespeitaLogBodiesDesligado(t *testing.T) {
	old := logBodies
	logBodies = false
	defer func() { logBodies = old }()
	buf := captureSlog(t)
	h := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if strings.Contains(buf.String(), "req_body") {
		t.Error("com LOG_BODIES desligado não deve haver req_body")
	}
}
