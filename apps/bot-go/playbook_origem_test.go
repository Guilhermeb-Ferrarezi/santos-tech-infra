package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// De onde veio a ficha (pedido do Henrique, 26/09/2026): "a ficha da Vivian
// aparece sem etiqueta e eu não sabia de onde tinha vindo". Origens:
// manual (escrita no painel), ia (Virar aprendizado), claude (escrita pelo
// Claude a pedido do Henrique — anotação, pois o Claude usa a sessão dele) e
// whatsapp (reservado). ia e whatsapp só o servidor atribui.

func TestOrigemClaudeNaCriacaoENaCorrecao(t *testing.T) {
	s, h := servidorDeVendas(t)
	ctx := context.Background()

	var f Situacao
	rec := pede(t, h, "POST", "/api/vendas/situacoes", `{"titulo":"escrita pelo Claude","origem":"claude"}`)
	_ = json.Unmarshal(rec.Body.Bytes(), &f)
	if rec.Code != 200 || f.Origem != "claude" {
		t.Fatalf("POST com origem claude deveria manter: %d %s", rec.Code, rec.Body)
	}
	rec = pede(t, h, "POST", "/api/vendas/situacoes", `{"titulo":"forjada","origem":"ia"}`)
	_ = json.Unmarshal(rec.Body.Bytes(), &f)
	if f.Origem != "manual" {
		t.Errorf("o painel não pode se dizer IA: veio %q", f.Origem)
	}

	// Ficha criada no painel (manual) pode ser corrigida para claude.
	manual, err := s.playbook.Cria(ctx, TenantID(s.cfg.TenantID), Situacao{Titulo: "Vivian"}, "painel")
	if err != nil {
		t.Fatal(err)
	}
	rec = pede(t, h, "PATCH", "/api/vendas/situacoes/"+manual.ID, `{"titulo":"Vivian","origem":"claude"}`)
	_ = json.Unmarshal(rec.Body.Bytes(), &f)
	if rec.Code != 200 || f.Origem != "claude" {
		t.Errorf("manual → claude deveria valer: %d %s", rec.Code, rec.Body)
	}
	// Edição sem origem não mexe nela.
	rec = pede(t, h, "PATCH", "/api/vendas/situacoes/"+manual.ID, `{"titulo":"Vivian editada"}`)
	_ = json.Unmarshal(rec.Body.Bytes(), &f)
	if f.Origem != "claude" {
		t.Errorf("editar sem origem mantém a origem: veio %q", f.Origem)
	}

	// Ficha da IA não vira outra coisa.
	ia, err := s.playbook.Cria(ctx, TenantID(s.cfg.TenantID), Situacao{Titulo: "da IA", Origem: "ia"}, "painel")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range []string{"claude", "manual", "whatsapp"} {
		rec = pede(t, h, "PATCH", "/api/vendas/situacoes/"+ia.ID, `{"titulo":"da IA","origem":"`+o+`"}`)
		_ = json.Unmarshal(rec.Body.Bytes(), &f)
		if f.Origem != "ia" {
			t.Errorf("ficha da IA não pode virar %q: veio %q", o, f.Origem)
		}
	}
	if !strings.Contains(pede(t, h, "GET", "/api/vendas/situacoes", "").Body.String(), `"origem":"claude"`) {
		t.Error("a lista deveria trazer a origem claude")
	}
}
