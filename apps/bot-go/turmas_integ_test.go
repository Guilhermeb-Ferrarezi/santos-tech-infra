package main

import (
	"context"
	"testing"
	"time"
)

// Integração do retrato contra Postgres real (migration 0045): roda só com
// BOT_TEST_DATABASE_URL; usa o mesmo tenant descartável das regras de venda.
func TestTurmaRetratoRepoIntegracao(t *testing.T) {
	pool, tenant := tenantRegrasDeTeste(t)
	repo := NewTurmaRetratoRepo(pool)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC)

	a := turmaAoVivoDeTeste()
	b := turmaAoVivoDeTeste()
	b.ID, b.Nome, b.Vagas = 23, "Turma Informática", 0
	b.Horarios = []HorarioTurma{{DiaSemana: 2, HoraInicio: "19:30", HoraFim: "21:30"}, {DiaSemana: 4, HoraInicio: "19:30", HoraFim: "21:30"}}
	if err := repo.Grava(ctx, tenant, []TurmaAoVivo{a, b}, t0); err != nil {
		t.Fatal(err)
	}
	lista, err := repo.Lista(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if len(lista) != 2 || lista[0].TurmaID != 22 || lista[1].Vagas != 0 || len(lista[1].Horarios) != 2 ||
		lista[0].Inicio != "2026-08-01" || lista[0].FimPrevisto != "2027-08-01" || lista[0].Curso != "Programação" ||
		!lista[0].CapturadoEm.Equal(t0) || lista[0].SumiuEm != nil {
		t.Fatalf("retrato gravado: %+v", lista)
	}

	// Segunda consulta: a 23 não veio → sumiu; a 22 atualiza vagas e hora.
	t1 := t0.Add(time.Hour)
	a.Vagas = 6
	if err := repo.Grava(ctx, tenant, []TurmaAoVivo{a}, t1); err != nil {
		t.Fatal(err)
	}
	lista, _ = repo.Lista(ctx, tenant)
	if lista[0].Vagas != 6 || !lista[0].CapturadoEm.Equal(t1) || lista[0].SumiuEm != nil {
		t.Fatalf("22 atualizada: %+v", lista[0])
	}
	if lista[1].SumiuEm == nil || !lista[1].SumiuEm.Equal(t1) {
		t.Fatalf("23 deveria estar marcada como sumida: %+v", lista[1])
	}

	// A 23 volta: sumiu_em limpa.
	if err := repo.Grava(ctx, tenant, []TurmaAoVivo{a, b}, t1.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	lista, _ = repo.Lista(ctx, tenant)
	if lista[1].SumiuEm != nil {
		t.Fatalf("23 voltou, sumiu_em deveria ser nulo: %+v", lista[1])
	}

	// Consulta vazia: todas somem (lista vazia = nenhuma liberada).
	if err := repo.Grava(ctx, tenant, []TurmaAoVivo{}, t1.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	lista, _ = repo.Lista(ctx, tenant)
	for _, r := range lista {
		if r.SumiuEm == nil {
			t.Fatalf("consulta vazia deveria marcar todas como sumidas: %+v", lista)
		}
	}
	// E o que o bot faz com isso: nada afirmável.
	if e := decideTurmas(nil, context.DeadlineExceeded, lista, t1.Add(3*time.Hour)); e.Origem != TurmasSemDados {
		t.Fatalf("todas sumidas: sem dados, got %+v", e)
	}

	// Isolamento por tenant.
	_, outro := tenantRegrasDeTeste(t)
	if l, _ := repo.Lista(ctx, outro); len(l) != 0 {
		t.Fatalf("outro tenant não vê o retrato: %+v", l)
	}
}
