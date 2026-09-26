package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// "Conversas para revisar" — o hábito que faz o playbook crescer: toda semana,
// as conversas que esfriaram e as aulas que não viraram matrícula.

func TestPrecisaRevisar(t *testing.T) {
	casos := []struct {
		q    Qualificacao
		quer string
	}{
		{Qualificacao{ParaQuem: "filho", AlunoIdade: 10, TurnosRespondendo: 2}, "morno"},
		{Qualificacao{PedidosDePreco: 1}, "frio"}, // só quis o preço
		{Qualificacao{}, ""}, // mandou "oi" e sumiu: ruído
		{Qualificacao{ParaQuem: "filho", AlunoIdade: 10, Interesse: "x", TurnosRespondendo: 2, AulaMarcada: true}, ""}, // qualificado
	}
	for i, c := range casos {
		if got := precisaRevisar(c.q); got != c.quer {
			t.Errorf("caso %d: esperado %q, veio %q", i, c.quer, got)
		}
	}
}

func TestRevisarIntegracao(t *testing.T) {
	s, h := servidorDeVendas(t)
	ctx := context.Background()
	pool, tenant := s.pool, s.cfg.TenantID
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	novo := func(nome, fone string, q Qualificacao, atualizado time.Time) (contato, conv string) {
		var ident string
		must(pool.QueryRow(ctx, `INSERT INTO contact (tenant_id, display_name) VALUES ($1, $2) RETURNING id`, tenant, nome).Scan(&contato))
		must(pool.QueryRow(ctx, `INSERT INTO channel_identity (tenant_id, contact_id, channel, external_id) VALUES ($1, $2, 'whatsapp', $3) RETURNING id`, tenant, contato, fone).Scan(&ident))
		must(pool.QueryRow(ctx, `INSERT INTO conversation (tenant_id, channel_identity_id, channel) VALUES ($1, $2, 'whatsapp') RETURNING id`, tenant, ident).Scan(&conv))
		_, err := pool.Exec(ctx, `INSERT INTO lead_qualificacao (tenant_id, contact_id, para_quem, aluno_idade, interesse, aula_marcada, turnos_respondendo, pedidos_de_preco, atualizado_em)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, tenant, contato, q.ParaQuem, q.AlunoIdade, q.Interesse, q.AulaMarcada, q.TurnosRespondendo, q.PedidosDePreco, atualizado)
		must(err)
		return
	}
	agora := time.Now()
	_, convMorno := novo("Morno", "5516911110001", Qualificacao{ParaQuem: "filho", AlunoIdade: 10, TurnosRespondendo: 2}, agora.Add(-24*time.Hour))
	novo("Antigo", "5516911110002", Qualificacao{ParaQuem: "filho", AlunoIdade: 10, TurnosRespondendo: 2}, agora.Add(-20*24*time.Hour))
	novo("Qualificado", "5516911110003", Qualificacao{ParaQuem: "filho", AlunoIdade: 10, Interesse: "x", TurnosRespondendo: 2, AulaMarcada: true}, agora)
	// Aula que não fechou, com o "por quê?" do Pós-aula: vem primeiro.
	_, convNaoFechou := novo("Vivian", "5516911110004", Qualificacao{ParaQuem: "filho", AlunoIdade: 10, Interesse: "x", TurnosRespondendo: 2, AulaMarcada: true}, agora)
	_, err := pool.Exec(ctx, `INSERT INTO aula_resultado (tenant_id, notion_page_id, client_phone, aula_em, resultado, observacao) VALUES ($1, 'pg-v', '5516911110004', $2, 'nao_fechou', 'achou caro')`, tenant, agora.Add(-48*time.Hour))
	must(err)

	rec := pede(t, h, "GET", "/api/vendas/revisar", "")
	if rec.Code != 200 {
		t.Fatalf("revisar: %d %s", rec.Code, rec.Body)
	}
	var r struct {
		Itens []ItemRevisar `json:"itens"`
		Dias  int           `json:"dias"`
	}
	must(json.Unmarshal(rec.Body.Bytes(), &r))
	if len(r.Itens) != 2 {
		t.Fatalf("esperado 2 conversas (não fechou + morno), veio %+v", r.Itens)
	}
	if r.Itens[0].Motivo != "nao_fechou" || r.Itens[0].ConversaID != convNaoFechou || r.Itens[0].Observacao != "achou caro" {
		t.Errorf("primeiro deveria ser a aula que não fechou, com o por quê: %+v", r.Itens[0])
	}
	if r.Itens[1].Motivo != "morno" || r.Itens[1].ConversaID != convMorno || r.Itens[1].Nome != "Morno" {
		t.Errorf("segundo deveria ser o lead morno: %+v", r.Itens[1])
	}
	if r.Dias != diasParaRevisar {
		t.Errorf("dias: %d", r.Dias)
	}
}
