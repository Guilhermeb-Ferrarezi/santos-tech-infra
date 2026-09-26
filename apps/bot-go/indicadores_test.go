package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Indicadores do playbook (spec 2026-09-25-bot-playbook-venda §4). A escola
// tem poucos leads por mês: abaixo de 20 casos, só contagem — porcentagem de
// 3 em 5 engana mais do que informa.

func TestContagemSoDaPercentualComVinteOuMais(t *testing.T) {
	if c := novaContagem(5, 19); c.Percentual != nil {
		t.Errorf("19 casos não dá porcentagem, veio %v", *c.Percentual)
	}
	c := novaContagem(5, 20)
	if c.Percentual == nil || *c.Percentual != 25 {
		t.Errorf("5 de 20 = 25%%, veio %v", c.Percentual)
	}
	if c := novaContagem(0, 0); c.Percentual != nil || c.N != 0 {
		t.Error("sem casos, sem porcentagem")
	}
}

func TestIndicadoresDosLeads(t *testing.T) {
	conversou := Qualificacao{ParaQuem: "filho", AlunoIdade: 10, Interesse: "jogos", TurnosRespondendo: 2}
	marcou := conversou
	marcou.AulaMarcada = true // qualificado: respondeu 3 e marcou
	valvula := Qualificacao{PrecoInformado: true, PedidosDePreco: 2, TurnosRespondendo: 1}
	depoisDaConversa := conversou
	depoisDaConversa.PrecoInformado = true
	mudo := Qualificacao{} // não respondeu nada

	ind := indicadoresDosLeads([]Qualificacao{conversou, marcou, valvula, depoisDaConversa, mudo})
	if ind.qualificados.N != 1 || ind.qualificados.Total != 5 {
		t.Errorf("qualificados: esperado 1 de 5, veio %+v", ind.qualificados)
	}
	if ind.precoValvula.N != 1 || ind.precoValvula.Total != 2 {
		t.Errorf("preço pela válvula: esperado 1 de 2 que souberam o preço, veio %+v", ind.precoValvula)
	}
	if ind.aulaMarcada.N != 1 || ind.aulaMarcada.Total != 4 {
		t.Errorf("aula marcada: esperado 1 de 4 que conversaram, veio %+v", ind.aulaMarcada)
	}
	// Perguntas respondidas por quem conversou: 3 + 3 + 0 + 3 = 9, em 4 leads.
	if ind.perguntas.Media == nil || *ind.perguntas.Media != 2.25 || ind.perguntas.Total != 4 {
		t.Errorf("perguntas por lead: esperado média 2.25 em 4, veio %+v", ind.perguntas)
	}
}

func TestIndicadorDasAulas(t *testing.T) {
	c, sem := indicadorDasAulas([]string{"fechou", "veio", "nao_fechou", "faltou", "", ""})
	if c.N != 1 || c.Total != 3 || sem != 2 {
		t.Errorf("fechou/veio: esperado 1 de 3 e 2 sem marcação; veio %+v, %d", c, sem)
	}
}

func TestPeriodoDosIndicadores(t *testing.T) {
	hoje := time.Date(2026, 9, 25, 15, 0, 0, 0, time.UTC)
	p, err := periodoDosIndicadores("", "", hoje)
	if err != nil {
		t.Fatal(err)
	}
	if p.De.Format("2006-01-02") != "2026-08-27" || p.Ate.Format("2006-01-02") != "2026-09-26" {
		t.Errorf("padrão = últimos 30 dias até hoje (fim exclusivo), veio %s → %s", p.De, p.Ate)
	}
	if p.AnteriorDe.Format("2006-01-02") != "2026-07-28" || !p.AnteriorAte.Equal(p.De) {
		t.Errorf("anterior = 30 dias antes, colado; veio %s → %s", p.AnteriorDe, p.AnteriorAte)
	}
	p, err = periodoDosIndicadores("2026-09-01", "2026-09-10", hoje)
	if err != nil || p.Ate.Format("2006-01-02") != "2026-09-11" || p.AnteriorDe.Format("2006-01-02") != "2026-08-22" {
		t.Errorf("período informado: %v %+v", err, p)
	}
	for _, ruim := range [][2]string{{"2026-09-10", "2026-09-01"}, {"ontem", ""}, {"2025-01-01", "2026-09-01"}} {
		if _, err := periodoDosIndicadores(ruim[0], ruim[1], hoje); err == nil {
			t.Errorf("período %v deveria ser recusado", ruim)
		}
	}
}

func TestIndicadoresIntegracao(t *testing.T) {
	s, h := servidorDeVendas(t)
	ctx := context.Background()
	pool := s.pool
	tenant := s.cfg.TenantID
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	dia := func(d string) time.Time { tt, _ := time.Parse("2006-01-02 15:04", d); return tt }

	// Dois leads no período atual (um qualificado), um no anterior.
	novoLead := func(fone string, criado time.Time, marcou bool) string {
		var contato, ident string
		must(pool.QueryRow(ctx, `INSERT INTO contact (tenant_id, display_name) VALUES ($1, 'c') RETURNING id`, tenant).Scan(&contato))
		must(pool.QueryRow(ctx, `INSERT INTO channel_identity (tenant_id, contact_id, channel, external_id) VALUES ($1, $2, 'whatsapp', $3) RETURNING id`, tenant, contato, fone).Scan(&ident))
		_, err := pool.Exec(ctx, `INSERT INTO lead_qualificacao (tenant_id, contact_id, para_quem, aluno_idade, interesse, aula_marcada, turnos_respondendo, criado_em)
			VALUES ($1, $2, 'filho', 10, 'jogos', $3, 2, $4)`, tenant, contato, marcou, criado)
		must(err)
		return contato
	}
	a := novoLead("5516900000001", dia("2026-09-20 10:00"), true)
	novoLead("5516900000002", dia("2026-09-21 10:00"), false)
	novoLead("5516900000003", dia("2026-08-20 10:00"), true)

	// Aula do lead A: fechou.
	_, err := pool.Exec(ctx, `INSERT INTO booking_reminder (tenant_id, notion_page_id, client_phone, kind, aula_em, enviar_em)
		VALUES ($1, 'pg-a', '5516900000001', 'experimental', $2, $2)`, tenant, dia("2026-09-22 15:00"))
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO aula_resultado (tenant_id, notion_page_id, client_phone, aula_em, resultado) VALUES ($1, 'pg-a', '5516900000001', $2, 'fechou')`, tenant, dia("2026-09-22 15:00"))
	must(err)

	// Ficha ativa usada na conversa do lead A.
	ficha, err := s.playbook.Cria(ctx, TenantID(tenant), Situacao{Titulo: "Gostou, mas vai espaçar", Estado: "ativa"}, "painel")
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO bot_playbook_uso (tenant_id, situacao_id, contact_id, conversation_id, usado_em) VALUES ($1, $2, $3, gen_random_uuid(), $4)`,
		tenant, ficha.ID, a, dia("2026-09-20 10:05"))
	must(err)

	rec := pede(t, h, "GET", "/api/vendas/indicadores?de=2026-09-01&ate=2026-09-30", "")
	if rec.Code != 200 {
		t.Fatalf("GET indicadores: %d %s", rec.Code, rec.Body)
	}
	var r RespostaIndicadores
	must(json.Unmarshal(rec.Body.Bytes(), &r))
	por := map[string]Indicador{}
	for _, i := range r.Indicadores {
		por[i.Chave] = i
	}
	if q := por["qualificados"]; q.Atual.N != 1 || q.Atual.Total != 2 || q.Anterior.Total != 1 {
		t.Errorf("qualificados: %+v", q)
	}
	if q := por["aulaMarcada"]; q.Atual.N != 1 || q.Atual.Total != 2 {
		t.Errorf("aula marcada: %+v", q)
	}
	if q := por["fechouVeio"]; q.Atual.N != 1 || q.Atual.Total != 1 || q.Atual.Percentual != nil {
		t.Errorf("fechou/veio (1 caso, sem %%): %+v", q)
	}
	if len(r.UsoPorFicha) != 1 || r.UsoPorFicha[0].Conversas != 1 || r.UsoPorFicha[0].Marcaram != 1 || r.UsoPorFicha[0].Fecharam != 1 {
		t.Errorf("uso por ficha: %+v", r.UsoPorFicha)
	}
	// A ficha foi criada e ativada agora: pedindo o período de hoje, a
	// ativação vira marca na linha do tempo; num período passado, não.
	temMarca := func(url string) bool {
		t.Helper()
		var rr RespostaIndicadores
		_ = json.Unmarshal(pede(t, h, "GET", url, "").Body.Bytes(), &rr)
		for _, m := range rr.Marcas {
			if m.Tipo == "ficha" && m.Acao == "ativou" && strings.Contains(m.Titulo, "espaçar") {
				return true
			}
		}
		return false
	}
	hoje := time.Now().Format("2006-01-02")
	if !temMarca("/api/vendas/indicadores?de=" + hoje + "&ate=" + hoje) {
		t.Error("a ativação da ficha hoje deveria virar marca na linha do tempo")
	}
	if temMarca("/api/vendas/indicadores?de=2026-01-01&ate=2026-01-31") {
		t.Error("marca fora do período não deveria aparecer")
	}

	if rec = pede(t, h, "GET", "/api/vendas/indicadores?de=2026-09-30&ate=2026-09-01", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("período invertido: esperado 400, veio %d", rec.Code)
	}
}
