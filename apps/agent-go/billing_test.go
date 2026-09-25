package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const chaveTeste = "sk-ant-api03-chave-de-teste-1234"

// fakeBilling guarda a linha de cobrança em memória (o Postgres real é testado à
// parte, contra um container). errGet simula o banco fora do ar.
type fakeBilling struct {
	row    billingRow
	errGet error
}

func (f *fakeBilling) get(context.Context) (billingRow, error) {
	if f.errGet != nil {
		return billingRow{}, f.errGet
	}
	return f.row, nil
}

func (f *fakeBilling) saveKey(_ context.Context, enc []byte, hint string) error {
	f.row.APIKeyEnc, f.row.Hint, f.row.UpdatedAt = enc, hint, time.Now()
	return nil
}

func (f *fakeBilling) clearKey(context.Context) error {
	f.row = billingRow{UpdatedAt: time.Now()}
	return nil
}

func (f *fakeBilling) setBotUsesAPIKey(_ context.Context, enabled bool) (int64, error) {
	if enabled && len(f.row.APIKeyEnc) == 0 {
		return 0, nil
	}
	f.row.BotUsesAPIKey = enabled
	return 1, nil
}

func serverComBilling(t *testing.T, fb *fakeBilling) *Server {
	t.Helper()
	return &Server{cfg: Config{EncryptionKey: "chave-de-cifra-teste"}, billing: fb}
}

func cifrada(t *testing.T, s *Server, texto string) []byte {
	t.Helper()
	enc, err := encrypt(s.cfg.EncryptionKey, texto)
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func TestCredencialPara(t *testing.T) {
	casos := []struct {
		nome    string
		uid     int64
		origin  string
		ligado  bool
		comChav bool
		lixo    bool // chave gravada mas indecifrável
		errGet  error
		querKey bool
	}{
		{nome: "bot interno com troca ligada usa a chave", uid: 0, origin: "bot", ligado: true, comChav: true, querKey: true},
		{nome: "tarefas do bot também usam a chave", uid: 0, origin: "bot-tarefas", ligado: true, comChav: true, querKey: true},
		{nome: "admin se passando pelo bot fica na assinatura", uid: 7, origin: "bot", ligado: true, comChav: true},
		{nome: "outra função fica na assinatura", uid: 0, origin: "posaula", ligado: true, comChav: true},
		{nome: "sem origem fica na assinatura", uid: 0, origin: "", ligado: true, comChav: true},
		{nome: "troca desligada fica na assinatura", uid: 0, origin: "bot", ligado: false, comChav: true},
		{nome: "ligada sem chave fica na assinatura", uid: 0, origin: "bot", ligado: true},
		{nome: "chave indecifrável cai na assinatura", uid: 0, origin: "bot", ligado: true, lixo: true},
		{nome: "banco fora do ar cai na assinatura", uid: 0, origin: "bot", errGet: errors.New("sem banco")},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			fb := &fakeBilling{errGet: c.errGet}
			s := serverComBilling(t, fb)
			fb.row.BotUsesAPIKey = c.ligado
			if c.comChav {
				fb.row.APIKeyEnc = cifrada(t, s, chaveTeste)
			}
			if c.lixo {
				fb.row.APIKeyEnc = []byte("isto-nao-e-cifra-valida-de-jeito-nenhum")
			}
			got := s.credencialPara(context.Background(), c.uid, c.origin)
			if c.querKey && got.apiKey != chaveTeste {
				t.Fatalf("esperava rodar com a chave, veio %+v", got)
			}
			if !c.querKey && got.apiKey != "" {
				t.Fatalf("esperava assinatura, veio chave")
			}
			wantBilling := billingSubscription
			if c.querKey {
				wantBilling = billingAPIKey
			}
			if got.billing() != wantBilling {
				t.Fatalf("billing = %q, esperava %q", got.billing(), wantBilling)
			}
		})
	}
}

// Só UMA credencial vai pro CLI: com a chave, o token da assinatura some do
// ambiente; sem ela, nenhuma ANTHROPIC_API_KEY do container vaza pra dentro.
func TestClaudeEnvComLevaUmaCredencialSo(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-assinatura")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api03-do-container-nao-pode-vazar")
	s := &Server{cfg: Config{}}

	comChave := strings.Join(s.claudeEnvCom(context.Background(), nil, credencial{apiKey: chaveTeste}), "\n")
	if !strings.Contains(comChave, "ANTHROPIC_API_KEY="+chaveTeste) {
		t.Fatal("com chave: ANTHROPIC_API_KEY ausente")
	}
	if strings.Contains(comChave, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatal("com chave: o token da assinatura não podia estar no ambiente")
	}

	assinatura := strings.Join(s.claudeEnvCom(context.Background(), nil, credencial{}), "\n")
	if !strings.Contains(assinatura, "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-assinatura") {
		t.Fatal("assinatura: token OAuth ausente")
	}
	if strings.Contains(assinatura, "ANTHROPIC_API_KEY") {
		t.Fatal("assinatura: ANTHROPIC_API_KEY do container vazou pro CLI")
	}
}

func TestBillingGetNuncaDevolveAChave(t *testing.T) {
	fb := &fakeBilling{}
	s := serverComBilling(t, fb)
	fb.row = billingRow{BotUsesAPIKey: true, APIKeyEnc: cifrada(t, s, chaveTeste), Hint: "1234", UpdatedAt: time.Now()}

	rec := httptest.NewRecorder()
	s.handleBillingGet(rec, httptest.NewRequest(http.MethodGet, "/claude/billing", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, chaveTeste) || strings.Contains(body, "sk-ant") {
		t.Fatalf("GET vazou a chave: %s", body)
	}
	var got billingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.BotUsesAPIKey || !got.APIKeyConfigured || got.APIKeyHint != "1234" {
		t.Fatalf("resposta inesperada: %+v", got)
	}
}

// anthropicFalsa responde ao GET /v1/models: 200 só pra chave válida.
func anthropicFalsa(t *testing.T, valida string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("anthropic-version") == "" {
			http.Error(w, "rota errada", http.StatusNotFound)
			return
		}
		if r.Header.Get("x-api-key") != valida {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func putChave(s *Server, corpo string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.handleBillingSetKey(rec, httptest.NewRequest(http.MethodPut, "/claude/billing/api-key", strings.NewReader(corpo)))
	return rec
}

func TestBillingSetKeyValidaNaAnthropicAntesDeGravar(t *testing.T) {
	fb := &fakeBilling{}
	s := serverComBilling(t, fb)
	s.cfg.AnthropicAPIURL = anthropicFalsa(t, chaveTeste).URL

	// Recusada pela Anthropic → 400 e nada gravado.
	rec := putChave(s, `{"apiKey":"sk-ant-api03-chave-errada-9999"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "INVALID_API_KEY") {
		t.Fatalf("chave recusada: esperava 400 INVALID_API_KEY, veio %d %s", rec.Code, rec.Body)
	}
	if len(fb.row.APIKeyEnc) != 0 {
		t.Fatal("chave recusada não podia ser gravada")
	}

	// Aceita → gravada cifrada, com a dica dos 4 últimos, e a resposta não ecoa a chave.
	rec = putChave(s, `{"apiKey":"  `+chaveTeste+`  "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("chave válida: status %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), chaveTeste) {
		t.Fatal("resposta ecoou a chave")
	}
	claro, err := decrypt(s.cfg.EncryptionKey, fb.row.APIKeyEnc)
	if err != nil || claro != chaveTeste {
		t.Fatalf("chave gravada errada: %q %v", claro, err)
	}
	if fb.row.Hint != "1234" {
		t.Fatalf("dica = %q, esperava 1234", fb.row.Hint)
	}
}

func TestBillingSetKeyRecusaFormatoSemIrNaAnthropic(t *testing.T) {
	chamou := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { chamou = true }))
	defer srv.Close()
	fb := &fakeBilling{}
	s := serverComBilling(t, fb)
	s.cfg.AnthropicAPIURL = srv.URL

	for _, corpo := range []string{`{}`, `{"apiKey":""}`, `{"apiKey":"nao-e-chave"}`, `{"apiKey":"sk-ant-oat01-token-de-assinatura"}`, `lixo`} {
		rec := putChave(s, corpo)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: esperava 400, veio %d", corpo, rec.Code)
		}
	}
	if chamou {
		t.Fatal("formato inválido não devia chegar na Anthropic")
	}
}

func TestBillingSetKeyAnthropicForaDoAr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	fb := &fakeBilling{}
	s := serverComBilling(t, fb)
	s.cfg.AnthropicAPIURL = srv.URL

	rec := putChave(s, `{"apiKey":"`+chaveTeste+`"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("esperava 502 com a Anthropic fora, veio %d", rec.Code)
	}
	if len(fb.row.APIKeyEnc) != 0 {
		t.Fatal("sem validar, não podia gravar")
	}
}

func TestBillingLigarSemChaveDa400(t *testing.T) {
	fb := &fakeBilling{}
	s := serverComBilling(t, fb)

	rec := httptest.NewRecorder()
	s.handleBillingSet(rec, httptest.NewRequest(http.MethodPut, "/claude/billing", strings.NewReader(`{"botUsesApiKey":true}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "API_KEY_MISSING") {
		t.Fatalf("esperava 400 API_KEY_MISSING, veio %d %s", rec.Code, rec.Body)
	}

	fb.row.APIKeyEnc = cifrada(t, s, chaveTeste)
	rec = httptest.NewRecorder()
	s.handleBillingSet(rec, httptest.NewRequest(http.MethodPut, "/claude/billing", strings.NewReader(`{"botUsesApiKey":true}`)))
	if rec.Code != http.StatusOK || !fb.row.BotUsesAPIKey {
		t.Fatalf("com chave devia ligar: %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	s.handleBillingSet(rec, httptest.NewRequest(http.MethodPut, "/claude/billing", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("sem o campo: esperava 400, veio %d", rec.Code)
	}
}

func TestBillingDeleteApagaEDesliga(t *testing.T) {
	fb := &fakeBilling{}
	s := serverComBilling(t, fb)
	fb.row = billingRow{BotUsesAPIKey: true, APIKeyEnc: cifrada(t, s, chaveTeste), Hint: "1234"}

	rec := httptest.NewRecorder()
	s.handleBillingDeleteKey(rec, httptest.NewRequest(http.MethodDelete, "/claude/billing/api-key", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if fb.row.BotUsesAPIKey || len(fb.row.APIKeyEnc) != 0 || fb.row.Hint != "" {
		t.Fatalf("não apagou tudo: %+v", fb.row)
	}
}

func TestRotasDeBillingExigemLogin(t *testing.T) {
	s := newTestServer()
	s.billing = &fakeBilling{}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	for _, rt := range []struct{ metodo, path string }{
		{http.MethodGet, "/claude/billing"},
		{http.MethodPut, "/claude/billing"},
		{http.MethodDelete, "/claude/billing/api-key"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(rt.metodo, rt.path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s sem login: esperava 401, veio %d", rt.metodo, rt.path, rec.Code)
		}
	}
}

// Ponta a ponta com um CLI falso que despeja o ambiente: o bot (serviço interno)
// com a troca ligada roda com a chave, e o uso é registrado com origem e billing.
func TestGenerateDoBotRodaComAChaveERegistraOrigem(t *testing.T) {
	tmp := t.TempDir()
	envFile := filepath.Join(tmp, "env.txt")
	bin := filepath.Join(tmp, "claude")
	script := "#!/bin/sh\nenv > '" + envFile + "'\ncat > /dev/null\n" +
		`printf '%s\n' '{"type":"result","subtype":"success","result":"oi","total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":1}}'` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-assinatura")

	fb := &fakeBilling{}
	s := serverComBilling(t, fb)
	s.cfg.WorkspaceRoot, s.cfg.ClaudeBin, s.cfg.DefaultModel = t.TempDir(), bin, "sonnet"
	fb.row = billingRow{BotUsesAPIKey: true, APIKeyEnc: cifrada(t, s, chaveTeste), Hint: "1234"}
	var registrados []usageMeta
	s.onUsage = func(m usageMeta, _ usageFields) { registrados = append(registrados, m) }

	chamar := func(uid int64, origin string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/claude/generate",
			strings.NewReader(`{"task":"raw","brief":"olá","origin":"`+origin+`"}`))
		req = req.WithContext(context.WithValue(req.Context(), userIDKey, uid))
		rec := httptest.NewRecorder()
		s.handleGenerate(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		b, err := os.ReadFile(envFile)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	env := chamar(0, "bot")
	if !strings.Contains(env, "ANTHROPIC_API_KEY="+chaveTeste) || strings.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatalf("bot interno devia rodar só com a chave; env:\n%s", env)
	}
	env = chamar(0, "posaula")
	if strings.Contains(env, "ANTHROPIC_API_KEY") || !strings.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatalf("pós-aula devia rodar na assinatura; env:\n%s", env)
	}

	if len(registrados) != 2 {
		t.Fatalf("esperava 2 registros de uso, veio %d", len(registrados))
	}
	if r := registrados[0]; r.origin != "bot" || r.billing != billingAPIKey || r.task != "raw" || r.source != "generate" {
		t.Fatalf("registro do bot errado: %+v", r)
	}
	if r := registrados[1]; r.origin != "posaula" || r.billing != billingSubscription {
		t.Fatalf("registro do pós-aula errado: %+v", r)
	}
}

func TestNormalizaOrigem(t *testing.T) {
	casos := map[string]string{
		"bot": "bot", " Bot ": "bot", "bot-tarefas": "bot-tarefas", "posaula": "posaula",
		"": "", "com espaço": "", "tem;ponto": "", strings.Repeat("a", 41): "",
	}
	for in, want := range casos {
		if got := normalizaOrigem(in); got != want {
			t.Fatalf("normalizaOrigem(%q) = %q, esperava %q", in, got, want)
		}
	}
}
