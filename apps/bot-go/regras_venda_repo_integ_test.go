package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Integração contra Postgres real: roda só com BOT_TEST_DATABASE_URL apontando
// para um banco com as migrations aplicadas; sem ela, pula.

func tenantRegrasDeTeste(t *testing.T) (*pgxpool.Pool, TenantID) {
	t.Helper()
	url := os.Getenv("BOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("BOT_TEST_DATABASE_URL vazio")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var tenant string
	if err := pool.QueryRow(ctx, `INSERT INTO tenants (name, slug) VALUES ('t', 't-'||gen_random_uuid()) RETURNING id`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	return pool, TenantID(tenant)
}

func TestRegrasVendaRepoSemLinhaEPadrao(t *testing.T) {
	pool, tenant := tenantRegrasDeTeste(t)
	repo := &RegrasVendaRepo{pool: pool}
	v, err := repo.Atual(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "" {
		t.Errorf("sem nada salvo, a versão atual deveria ser vazia, veio %q", v.ID)
	}
	if got := v.Regras.Resolvida().Faixas; got != PadraoRegrasVenda().Faixas {
		t.Errorf("sem nada salvo deveria valer o padrão, veio %+v", got)
	}
}

func TestRegrasVendaRepoSalvaNormalizaEVersiona(t *testing.T) {
	pool, tenant := tenantRegrasDeTeste(t)
	repo := &RegrasVendaRepo{pool: pool}
	ctx := context.Background()

	p := PadraoRegrasVenda()
	doc := p // igual ao padrão, menos uma parte e a faixa do particular
	doc.Textos = map[ParteRegra]string{}
	for k, v := range p.Textos {
		doc.Textos[k] = v
	}
	doc.Textos[ParteDecidir] = "meu jeito de decidir"
	doc.Faixas.IdadeParticular = 18

	v1, err := repo.Salvar(ctx, tenant, doc, "", "Henrique")
	if err != nil {
		t.Fatal(err)
	}
	// O que é igual ao padrão é gravado vazio.
	if len(v1.Regras.Textos) != 1 || v1.Regras.Textos[ParteDecidir] != "meu jeito de decidir" {
		t.Errorf("só a parte personalizada deveria ser gravada, veio %v", v1.Regras.Textos)
	}
	if v1.Regras.Vocabulario != nil {
		t.Error("vocabulário igual ao padrão deveria ser gravado vazio")
	}
	if v1.Regras.Faixas.IdadeParticular != 18 {
		t.Errorf("faixa personalizada perdida: %+v", v1.Regras.Faixas)
	}
	if v1.SalvoPor != "Henrique" || v1.PadraoHash[ParteDecidir] == "" {
		t.Errorf("versão sem autor ou sem hash do padrão: %+v", v1)
	}

	// Salvar em cima de versão velha é recusado (409 na API).
	if _, err := repo.Salvar(ctx, tenant, doc, "", "Rodrigo"); !errors.Is(err, ErrVersaoDesatualizada) {
		t.Errorf("save com base vazia depois de existir versão deveria dar ErrVersaoDesatualizada, veio %v", err)
	}

	doc2 := RegrasVenda{Faixas: FaixasIdade{IdadeParticular: 19, IdadeFimFaixaTurma: 15}}
	v2, err := repo.Salvar(ctx, tenant, doc2, v1.ID, "Henrique")
	if err != nil {
		t.Fatal(err)
	}
	atual, err := repo.Atual(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if atual.ID != v2.ID || atual.Regras.Faixas.IdadeParticular != 19 {
		t.Errorf("a atual deveria ser a última salva, veio %+v", atual)
	}
	if !contem(v2.Alteracoes, "faixas") || !contem(v2.Alteracoes, string(ParteDecidir)) {
		t.Errorf("alterações da v2 deveriam citar faixas e decidir, veio %v", v2.Alteracoes)
	}

	// Inválido não grava.
	if _, err := repo.Salvar(ctx, tenant, RegrasVenda{Faixas: FaixasIdade{IdadeParticular: 10, IdadeFimFaixaTurma: 15}}, v2.ID, "x"); !errors.Is(err, ErrRegrasInvalidas) {
		t.Errorf("faixa invertida deveria dar ErrRegrasInvalidas, veio %v", err)
	}

	// Restaurar a v1 cria uma v3 com o conteúdo dela, sem apagar nada.
	v3, err := repo.Restaurar(ctx, tenant, v1.ID, "Henrique")
	if err != nil {
		t.Fatal(err)
	}
	if v3.RestauradaDe != v1.ID || v3.Regras.Textos[ParteDecidir] != "meu jeito de decidir" {
		t.Errorf("restauração errada: %+v", v3)
	}
	lista, err := repo.Versoes(ctx, tenant, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(lista) != 3 || lista[0].ID != v3.ID || lista[2].ID != v1.ID {
		t.Errorf("histórico deveria ter 3 versões, da mais nova para a mais velha: %+v", lista)
	}

	// Versão de outro tenant não existe para este.
	_, outro := tenantRegrasDeTeste(t)
	if _, err := repo.Versao(ctx, outro, v1.ID); !errors.Is(err, ErrVersaoNaoEncontrada) {
		t.Errorf("versão de outro tenant deveria dar ErrVersaoNaoEncontrada, veio %v", err)
	}
	if _, err := repo.Restaurar(ctx, outro, v1.ID, "x"); !errors.Is(err, ErrVersaoNaoEncontrada) {
		t.Errorf("restaurar versão de outro tenant deveria dar ErrVersaoNaoEncontrada, veio %v", err)
	}
}
