package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

type leitorFichasFalso struct {
	fichas   []Situacao
	err      error
	leituras int
}

func (l *leitorFichasFalso) Ativas(ctx context.Context, tenant TenantID) ([]Situacao, error) {
	l.leituras++
	return l.fichas, l.err
}

func TestFonteDoPlaybookCacheiaInvalidaENuncaFalha(t *testing.T) {
	l := &leitorFichasFalso{fichas: []Situacao{{ID: "a", Estado: "ativa"}}}
	agora := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	f := NewPlaybookFonte(l, 30*time.Second, nil)
	f.agora = func() time.Time { return agora }
	ctx := context.Background()

	if got := f.Ativas(ctx, "t"); len(got) != 1 {
		t.Fatalf("deveria devolver as ativas, veio %v", got)
	}
	f.Ativas(ctx, "t")
	if l.leituras != 1 {
		t.Errorf("dentro da validade não relê; leu %d", l.leituras)
	}
	f.Invalida("t")
	l.err = errors.New("banco fora")
	if got := f.Ativas(ctx, "t"); len(got) != 1 {
		t.Errorf("banco fora: vale a última lida; veio %v", got)
	}
	g := NewPlaybookFonte(&leitorFichasFalso{err: errors.New("fora")}, time.Minute, nil)
	if got := g.Ativas(ctx, "t"); len(got) != 0 {
		t.Errorf("sem leitura nenhuma: nenhuma ficha (o bot segue só com os princípios); veio %v", got)
	}
	var nulo *PlaybookFonte
	if got := nulo.Ativas(ctx, "t"); got != nil {
		t.Error("fonte nil devolve nada")
	}
}
