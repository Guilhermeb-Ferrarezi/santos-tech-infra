package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Integração contra Postgres real (o go test comum não valida SQL — já derrubou
// produção uma vez). Roda só com BOT_TEST_DATABASE_URL apontando para um banco
// com as migrations aplicadas; sem ela, pula.
func TestRetornoIntegracaoGravaEDisparaNaHoraCerta(t *testing.T) {
	url := os.Getenv("BOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("BOT_TEST_DATABASE_URL vazio")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var tenant, contact, ident, conv string
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, `INSERT INTO tenants (name, slug) VALUES ('t', 't-'||gen_random_uuid()) RETURNING id`).Scan(&tenant))
	must(pool.QueryRow(ctx, `INSERT INTO contact (tenant_id, display_name) VALUES ($1, 'Vivian Moraes') RETURNING id`, tenant).Scan(&contact))
	must(pool.QueryRow(ctx, `INSERT INTO channel_identity (tenant_id, contact_id, channel, external_id) VALUES ($1, $2, 'whatsapp', '5511999990000') RETURNING id`, tenant, contact).Scan(&ident))
	must(pool.QueryRow(ctx, `INSERT INTO conversation (tenant_id, channel_identity_id, channel) VALUES ($1, $2, 'whatsapp') RETURNING id`, tenant, ident).Scan(&conv))
	_, err = pool.Exec(ctx, `INSERT INTO lead_summary (tenant_id, contact_id, summary) VALUES ($1, $2, 'Gostou da experimental.')`, tenant, contact)
	must(err)

	// Grava pelo mesmo caminho do engine, com data já vencida.
	repo := &ConversationRepo{pool: pool}
	tx, err := pool.Begin(ctx)
	must(err)
	sc := &ScheduledContact{RawPhrase: "em dezembro consigo mais", ResolvedDate: "2026-09-20", Confidence: 0.5}
	must(repo.SaveReactivation(ctx, tx, ConversationID(conv), TenantID(tenant), ContactID(contact), sc))
	must(tx.Commit(ctx))

	var fire time.Time
	must(pool.QueryRow(ctx, `SELECT fire_at FROM scheduled_contacts WHERE conversation_id = $1`, conv).Scan(&fire))
	sp, _ := time.LoadLocation("America/Sao_Paulo")
	if got := fire.In(sp).Format("2006-01-02 15:04"); got != "2026-09-20 09:00" {
		t.Errorf("fire_at = %s em Brasília, esperado 2026-09-20 09:00", got)
	}

	sched := &ScheduledContactRepo{pool: pool}
	ps, err := sched.PendingReactivations(ctx, 10)
	must(err)
	var achou *retornoPendente
	for i := range ps {
		if string(ps[i].ConvID) == conv {
			achou = &ps[i]
		}
	}
	if achou == nil {
		t.Fatal("o retorno vencido não apareceu na fila")
	}
	if achou.Frase != "em dezembro consigo mais" || achou.Nome != "Vivian Moraes" ||
		achou.Telefone != "5511999990000" || achou.Resumo != "Gostou da experimental." || achou.Confianca != 0.5 {
		t.Errorf("campos errados: %+v", *achou)
	}

	// Depois de marcado, não volta.
	must(sched.MarkFollowUpSent(ctx, achou.ID))
	ps, err = sched.PendingReactivations(ctx, 10)
	must(err)
	for _, p := range ps {
		if string(p.ConvID) == conv {
			t.Error("retorno já disparado voltou para a fila")
		}
	}
}
