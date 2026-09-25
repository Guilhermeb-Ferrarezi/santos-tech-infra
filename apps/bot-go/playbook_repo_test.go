package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSituacaoValida(t *testing.T) {
	ok := Situacao{Titulo: "Gostou, mas vai espaçar", Motivos: []string{"pessoal"}, ParaQuem: "proprio"}
	if err := ok.Valida(); err != nil {
		t.Fatalf("ficha válida recusada: %v", err)
	}
	grande := strings.Repeat("a", maxCampoSituacao+1)
	ruins := map[string]Situacao{
		"sem título":       {Titulo: "  "},
		"título grande":    {Titulo: strings.Repeat("a", maxTituloSituacao+1)},
		"campo grande":     {Titulo: "x", Conduzir: grande},
		"motivo inventado": {Titulo: "x", Motivos: []string{"dinheiro"}},
		"para quem errado": {Titulo: "x", ParaQuem: "avo"},
		"estado inventado": {Titulo: "x", Estado: "publicada"},
		"origem inventada": {Titulo: "x", Origem: "email"},
	}
	for nome, s := range ruins {
		if err := s.Valida(); err == nil {
			t.Errorf("%s: deveria ser recusada", nome)
		}
	}
}

func TestPlaybookRepoCriaEditaMudaEstadoERegistraEventos(t *testing.T) {
	pool, tenant := tenantRegrasDeTeste(t)
	repo := &PlaybookRepo{pool: pool}
	ctx := context.Background()

	s, err := repo.Cria(ctx, tenant, Situacao{
		Titulo:   "Gostou da aula, mas vai espaçar",
		Sinais:   "vou espaçar; em dezembro consigo mais",
		Conduzir: "registre a data do retorno; não pressione",
		Motivos:  []string{"pessoal"},
	}, "painel")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID == "" || s.Estado != "rascunho" || s.Origem != "manual" || s.CriadoPor != "painel" {
		t.Errorf("ficha nova deveria nascer rascunho/manual com autor: %+v", s)
	}
	if ativas, _ := repo.Ativas(ctx, tenant); len(ativas) != 0 {
		t.Error("rascunho não pode estar entre as ativas")
	}

	s.Estado = "ativa"
	s.Conduzir = "registre a data do retorno e mantenha o vínculo"
	s2, err := repo.Edita(ctx, tenant, s.ID, s, "painel")
	if err != nil {
		t.Fatal(err)
	}
	if s2.Estado != "ativa" || !strings.Contains(s2.Conduzir, "vínculo") {
		t.Errorf("edição não gravou: %+v", s2)
	}
	ativas, err := repo.Ativas(ctx, tenant)
	if err != nil || len(ativas) != 1 || ativas[0].ID != s.ID {
		t.Errorf("a ficha ativada deveria estar entre as ativas: %v %+v", err, ativas)
	}

	s2.Estado = "arquivada"
	if _, err := repo.Edita(ctx, tenant, s.ID, s2, "painel"); err != nil {
		t.Fatal(err)
	}
	todas, _ := repo.Lista(ctx, tenant, "")
	if len(todas) != 1 {
		t.Errorf("arquivar não apaga: esperado 1 ficha, veio %d", len(todas))
	}
	if arquivadas, _ := repo.Lista(ctx, tenant, "arquivada"); len(arquivadas) != 1 {
		t.Error("filtro por estado não achou a arquivada")
	}

	eventos, err := repo.Eventos(ctx, tenant, 50)
	if err != nil {
		t.Fatal(err)
	}
	var acoes []string
	for _, e := range eventos {
		acoes = append(acoes, e.Acao)
	}
	// Do mais novo para o mais velho.
	if strings.Join(acoes, ",") != "arquivou,ativou,criou" {
		t.Errorf("eventos esperados arquivou,ativou,criou — veio %v", acoes)
	}

	// Ficha de outro tenant não existe para este.
	_, outro := tenantRegrasDeTeste(t)
	if _, err := repo.Edita(ctx, outro, s.ID, s2, "x"); !errors.Is(err, ErrSituacaoNaoEncontrada) {
		t.Errorf("editar ficha de outro tenant deveria dar ErrSituacaoNaoEncontrada, veio %v", err)
	}
	if _, err := repo.Edita(ctx, tenant, "nao-e-uuid", s2, "x"); !errors.Is(err, ErrSituacaoNaoEncontrada) {
		t.Errorf("id malformado deveria dar ErrSituacaoNaoEncontrada, veio %v", err)
	}
	if _, err := repo.Cria(ctx, tenant, Situacao{Titulo: ""}, "x"); !errors.Is(err, ErrSituacaoInvalida) {
		t.Errorf("ficha sem título deveria dar ErrSituacaoInvalida, veio %v", err)
	}
}

func TestPlaybookRepoRegistraUsoSoDeFichasDoTenant(t *testing.T) {
	pool, tenant := tenantRegrasDeTeste(t)
	repo := &PlaybookRepo{pool: pool}
	ctx := context.Background()
	s, err := repo.Cria(ctx, tenant, Situacao{Titulo: "x", Estado: "ativa"}, "painel")
	if err != nil {
		t.Fatal(err)
	}
	contato, conversa := "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"
	if err := repo.RegistraUso(ctx, tenant, contato, conversa, []string{s.ID}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_playbook_uso WHERE tenant_id = $1`, tenant).Scan(&n); err != nil || n != 1 {
		t.Errorf("uso não registrado: %v %d", err, n)
	}
	// Ficha de outro tenant: ignorada, sem erro.
	_, outro := tenantRegrasDeTeste(t)
	if err := repo.RegistraUso(ctx, outro, contato, conversa, []string{s.ID}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_playbook_uso WHERE tenant_id = $1`, outro).Scan(&n); err != nil || n != 0 {
		t.Errorf("uso de ficha de outro tenant não pode ser gravado: %v %d", err, n)
	}
}
