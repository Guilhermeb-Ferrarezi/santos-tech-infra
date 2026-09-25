package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestRotasDoPlaybookFluxoCompleto(t *testing.T) {
	s, h := servidorDeVendas(t)
	ctx := context.Background()
	tenant := TenantID(s.cfg.TenantID)

	rec := pede(t, h, "GET", "/api/vendas/situacoes", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"situacoes":[]`) || !strings.Contains(rec.Body.String(), `"mercado_filho"`) {
		t.Fatalf("lista vazia deveria vir com a lista de motivos: %d %s", rec.Code, rec.Body)
	}

	// Aquece o cache do bot, para provar que criar invalida.
	s.playbookFonte.Ativas(ctx, tenant)

	rec = pede(t, h, "POST", "/api/vendas/situacoes",
		`{"titulo":"Gostou, mas vai espaçar","sinais":"vou espaçar","estado":"ativa","origem":"ia","motivos":["pessoal"]}`)
	if rec.Code != 200 {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	var nova Situacao
	_ = json.Unmarshal(rec.Body.Bytes(), &nova)
	if nova.ID == "" || nova.Origem != "manual" || nova.CriadoPor != "integração" {
		t.Errorf("ficha criada pelo painel é 'manual' e o autor vem da sessão: %+v", nova)
	}
	if got := s.playbookFonte.Ativas(ctx, tenant); len(got) != 1 {
		t.Errorf("depois de criar ativa, o bot deveria vê-la já na próxima mensagem; viu %d", len(got))
	}

	nova.Estado = "arquivada"
	corpo, _ := json.Marshal(nova)
	if rec = pede(t, h, "PATCH", "/api/vendas/situacoes/"+nova.ID, string(corpo)); rec.Code != 200 {
		t.Errorf("PATCH: %d %s", rec.Code, rec.Body)
	}
	if got := s.playbookFonte.Ativas(ctx, tenant); len(got) != 0 {
		t.Errorf("arquivada sai do bot na próxima mensagem; viu %d", len(got))
	}
	if rec = pede(t, h, "GET", "/api/vendas/situacoes?estado=arquivada", ""); !strings.Contains(rec.Body.String(), nova.ID) {
		t.Errorf("filtro por estado: %s", rec.Body)
	}

	for nome, c := range map[string]struct {
		metodo, url, corpo string
		status             int
	}{
		"sem título":      {"POST", "/api/vendas/situacoes", `{"titulo":" "}`, http.StatusBadRequest},
		"json quebrado":   {"POST", "/api/vendas/situacoes", `{`, http.StatusBadRequest},
		"motivo inválido": {"PATCH", "/api/vendas/situacoes/" + nova.ID, `{"titulo":"x","motivos":["dinheiro"]}`, http.StatusBadRequest},
		"inexistente":     {"PATCH", "/api/vendas/situacoes/00000000-0000-0000-0000-000000000000", `{"titulo":"x"}`, http.StatusNotFound},
		"estado inválido": {"GET", "/api/vendas/situacoes?estado=publicada", "", http.StatusBadRequest},
	} {
		if rec = pede(t, h, c.metodo, c.url, c.corpo); rec.Code != c.status {
			t.Errorf("%s: esperado %d, veio %d %s", nome, c.status, rec.Code, rec.Body)
		}
	}
}
