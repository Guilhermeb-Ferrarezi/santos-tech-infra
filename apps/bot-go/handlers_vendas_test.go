package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Rotas da tela "Como o bot vende" (regras de venda). As que tocam o banco
// rodam só com BOT_TEST_DATABASE_URL (ver regras_venda_repo_integ_test.go).

func servidorDeVendas(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	pool, tenant := tenantRegrasDeTeste(t)
	repo := &RegrasVendaRepo{pool: pool}
	s := &Server{
		cfg:         Config{TenantID: string(tenant), DashAPIKey: "chave-de-teste"},
		pool:        pool,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		session:     NewSessionAuth("http://127.0.0.1:1/auth/me", 100*time.Millisecond, time.Minute),
		regrasVenda: repo,
		regrasFonte: NewRegrasVendaFonte(repo, time.Minute, nil),
	}
	mux := http.NewServeMux()
	s.rotasDeVendas(mux)
	return s, mux
}

func pede(t *testing.T, h http.Handler, metodo, url, corpo string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(metodo, url, strings.NewReader(corpo))
	req.Header.Set("X-Dash-Key", "chave-de-teste")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type respostaRegras struct {
	Versao *struct {
		ID string `json:"id"`
	} `json:"versao"`
	Regras      RegrasVenda  `json:"regras"`
	Padrao      RegrasVenda  `json:"padrao"`
	Travas      []string     `json:"travas"`
	Partes      []ParteRegra `json:"partes"`
	PadraoMudou []ParteRegra `json:"padraoMudou"`
}

func TestRotasDeVendasSemCredencialDa401(t *testing.T) {
	s := &Server{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		session: NewSessionAuth("http://127.0.0.1:1/auth/me", 100*time.Millisecond, time.Minute),
	}
	mux := http.NewServeMux()
	s.rotasDeVendas(mux)
	for _, rota := range []struct{ metodo, url string }{
		{"GET", "/api/vendas/regras"},
		{"PATCH", "/api/vendas/regras"},
		{"GET", "/api/vendas/regras/versoes"},
		{"GET", "/api/vendas/regras/versoes/x"},
		{"POST", "/api/vendas/regras/versoes/x/restaurar"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(rota.metodo, rota.url, strings.NewReader("{}")))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s sem credencial: esperado 401, veio %d", rota.metodo, rota.url, rec.Code)
		}
	}
}

func TestRotasDeVendasFluxoCompleto(t *testing.T) {
	s, h := servidorDeVendas(t)

	// Nada salvo: vale o padrão, e a tela recebe padrão, travas e partes.
	rec := pede(t, h, "GET", "/api/vendas/regras", "")
	if rec.Code != 200 {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	var r0 respostaRegras
	if err := json.Unmarshal(rec.Body.Bytes(), &r0); err != nil {
		t.Fatal(err)
	}
	if r0.Versao != nil || r0.Padrao.Faixas.IdadeParticular != 17 || len(r0.Travas) != len(TravasDeHonestidade) || len(r0.Partes) != len(PartesDasRegras) {
		t.Errorf("GET sem nada salvo veio errado: %+v", r0)
	}

	// Aquece o cache do bot, para provar que o PATCH invalida.
	s.regrasFonte.Regras(context.Background(), TenantID(s.cfg.TenantID))

	rec = pede(t, h, "PATCH", "/api/vendas/regras",
		`{"versaoBase":"","regras":{"faixas":{"idadeParticular":18,"idadeFimFaixaTurma":15},"textos":{"decidir":"meu texto"}}}`)
	if rec.Code != 200 {
		t.Fatalf("PATCH: %d %s", rec.Code, rec.Body)
	}
	var r1 respostaRegras
	_ = json.Unmarshal(rec.Body.Bytes(), &r1)
	if r1.Versao == nil || r1.Versao.ID == "" || r1.Regras.Textos[ParteDecidir] != "meu texto" {
		t.Fatalf("PATCH deveria devolver a versão nova: %s", rec.Body)
	}
	if got := s.regrasFonte.Regras(context.Background(), TenantID(s.cfg.TenantID)); got.Faixas.IdadeParticular != 18 {
		t.Errorf("depois do PATCH o bot deveria ver a versão nova já na próxima mensagem, viu %+v", got.Faixas)
	}

	// Base velha: 409.
	rec = pede(t, h, "PATCH", "/api/vendas/regras", `{"versaoBase":"","regras":{}}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("base velha: esperado 409, veio %d %s", rec.Code, rec.Body)
	}

	// Inválido: 400 com mensagem em português.
	rec = pede(t, h, "PATCH", "/api/vendas/regras",
		`{"versaoBase":"`+r1.Versao.ID+`","regras":{"faixas":{"idadeParticular":14,"idadeFimFaixaTurma":15}}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "precisa ser menor") {
		t.Errorf("faixa invertida: esperado 400 com mensagem, veio %d %s", rec.Code, rec.Body)
	}

	// JSON quebrado: 400.
	if rec = pede(t, h, "PATCH", "/api/vendas/regras", `{`); rec.Code != http.StatusBadRequest {
		t.Errorf("JSON quebrado: esperado 400, veio %d", rec.Code)
	}

	// Corpo enorme: 413.
	grande := `{"versaoBase":"","regras":{"textos":{"decidir":"` + strings.Repeat("a", maxCorpoRegras) + `"}}}`
	if rec = pede(t, h, "PATCH", "/api/vendas/regras", grande); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("corpo enorme: esperado 413, veio %d", rec.Code)
	}

	// Histórico, uma versão e restauração.
	rec = pede(t, h, "GET", "/api/vendas/regras/versoes", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), r1.Versao.ID) {
		t.Errorf("versões: %d %s", rec.Code, rec.Body)
	}
	if rec = pede(t, h, "GET", "/api/vendas/regras/versoes/"+r1.Versao.ID, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "meu texto") {
		t.Errorf("uma versão: %d %s", rec.Code, rec.Body)
	}
	for _, id := range []string{"nao-e-uuid", "00000000-0000-0000-0000-000000000000"} {
		if rec = pede(t, h, "GET", "/api/vendas/regras/versoes/"+id, ""); rec.Code != http.StatusNotFound {
			t.Errorf("versão %s: esperado 404, veio %d", id, rec.Code)
		}
		if rec = pede(t, h, "POST", "/api/vendas/regras/versoes/"+id+"/restaurar", ""); rec.Code != http.StatusNotFound {
			t.Errorf("restaurar %s: esperado 404, veio %d", id, rec.Code)
		}
	}
	rec = pede(t, h, "POST", "/api/vendas/regras/versoes/"+r1.Versao.ID+"/restaurar", "")
	if rec.Code != 200 {
		t.Errorf("restaurar: %d %s", rec.Code, rec.Body)
	}

	// Padrão mudou depois da edição: finge que o padrão de "decidir" era outro
	// na hora em que foi salvo.
	if _, err := s.pool.Exec(context.Background(),
		`UPDATE bot_regras_venda_versao SET padrao_hash = jsonb_set(padrao_hash, '{decidir}', '"antigo"') WHERE tenant_id = $1`,
		s.cfg.TenantID); err != nil {
		t.Fatal(err)
	}
	rec = pede(t, h, "GET", "/api/vendas/regras", "")
	var r2 respostaRegras
	_ = json.Unmarshal(rec.Body.Bytes(), &r2)
	if len(r2.PadraoMudou) != 1 || r2.PadraoMudou[0] != ParteDecidir {
		t.Errorf("deveria avisar que o padrão de 'decidir' mudou, veio %v", r2.PadraoMudou)
	}
}
