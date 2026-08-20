package golog

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
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

// O agrupamento existe pra um caso concreto: um PC com credencial velha
// batendo 401 a cada 30s gerava 120 linhas/hora idênticas — 91% de tudo que o
// serviço logava. A primeira precisa sair na hora (senão o problema demora a
// aparecer) e a contagem não pode se perder (senão "aconteceu 87 vezes" vira
// "aconteceu").
func TestShouldLogRepeatAgrupaDentroDaJanela(t *testing.T) {
	repeatSeen = map[string]*repeatState{}
	const janela = time.Minute
	t0 := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	if ok, n := shouldLogRepeat("k", t0, janela); !ok || n != 0 {
		t.Fatalf("primeira ocorrência: ok=%v n=%d, quer true/0", ok, n)
	}
	// Repetições dentro da janela não viram linha.
	for i := 1; i <= 5; i++ {
		if ok, _ := shouldLogRepeat("k", t0.Add(time.Duration(i)*time.Second), janela); ok {
			t.Fatalf("repetição %d dentro da janela virou linha", i)
		}
	}
	// Fechada a janela, sai UMA linha com o que ficou pra trás.
	ok, n := shouldLogRepeat("k", t0.Add(janela+time.Second), janela)
	if !ok {
		t.Fatal("depois da janela a linha tem que sair")
	}
	if n != 5 {
		t.Fatalf("repeated=%d, quer 5 (as suprimidas)", n)
	}
	// E a contagem zera para o próximo ciclo.
	if ok, n := shouldLogRepeat("k", t0.Add(2*janela+2*time.Second), janela); !ok || n != 0 {
		t.Fatalf("segundo ciclo: ok=%v n=%d, quer true/0", ok, n)
	}
}

// Assinaturas diferentes (outro PC, outra rota, outro status) não podem se
// agrupar entre si — senão um problema esconde o outro.
func TestShouldLogRepeatNaoMisturaAssinaturas(t *testing.T) {
	repeatSeen = map[string]*repeatState{}
	t0 := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	for _, k := range []string{"POST /a 401 1.1.1.1", "POST /a 401 2.2.2.2", "POST /b 401 1.1.1.1", "POST /a 403 1.1.1.1"} {
		if ok, _ := shouldLogRepeat(k, t0, time.Minute); !ok {
			t.Errorf("%q deveria sair: é a primeira dessa assinatura", k)
		}
	}
}

// Janela 0 = agrupamento desligado: tudo passa.
func TestShouldLogRepeatDesligado(t *testing.T) {
	repeatSeen = map[string]*repeatState{}
	t0 := time.Now()
	for i := 0; i < 3; i++ {
		if ok, _ := shouldLogRepeat("k", t0, 0); !ok {
			t.Fatal("com janela 0 nada pode ser suprimido")
		}
	}
}

// O mapa não pode crescer sem teto: a chave inclui o IP, então uma varredura
// de rotas por IPs aleatórios encheria a memória do processo.
func TestShouldLogRepeatNaoCresceSemLimite(t *testing.T) {
	repeatSeen = map[string]*repeatState{}
	t0 := time.Now()
	for i := 0; i < repeatSeenMax+500; i++ {
		shouldLogRepeat("chave-"+strconv.Itoa(i), t0, time.Minute)
	}
	if len(repeatSeen) > repeatSeenMax {
		t.Fatalf("mapa com %d entradas, teto é %d", len(repeatSeen), repeatSeenMax)
	}
}
