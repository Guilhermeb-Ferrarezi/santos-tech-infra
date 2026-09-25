package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type leitorRegrasFalso struct {
	versao   VersaoRegras
	err      error
	leituras int
}

func (l *leitorRegrasFalso) Atual(ctx context.Context, tenant TenantID) (VersaoRegras, error) {
	l.leituras++
	return l.versao, l.err
}

func TestFonteDeRegrasCacheiaEInvalida(t *testing.T) {
	l := &leitorRegrasFalso{versao: VersaoRegras{ID: "v1", Regras: RegrasVenda{Faixas: FaixasIdade{IdadeParticular: 18}}}}
	agora := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	f := NewRegrasVendaFonte(l, 30*time.Second, nil)
	f.agora = func() time.Time { return agora }
	ctx := context.Background()

	if r := f.Regras(ctx, "t1"); r.Faixas.IdadeParticular != 18 {
		t.Fatalf("deveria devolver o que está salvo, veio %+v", r.Faixas)
	}
	f.Regras(ctx, "t1")
	if l.leituras != 1 {
		t.Errorf("dentro da validade não deveria ler de novo, leu %d vezes", l.leituras)
	}
	agora = agora.Add(31 * time.Second)
	f.Regras(ctx, "t1")
	if l.leituras != 2 {
		t.Errorf("vencida a validade deveria ler de novo, leu %d vezes", l.leituras)
	}
	l.versao.Regras.Faixas.IdadeParticular = 19
	f.Invalida("t1")
	if r := f.Regras(ctx, "t1"); r.Faixas.IdadeParticular != 19 {
		t.Errorf("depois de salvar (Invalida) a próxima mensagem já usa a versão nova, veio %+v", r.Faixas)
	}
}

// O bot nunca fica sem regra: banco fora do ar devolve a última lida, e sem
// nenhuma lida, o padrão.
func TestFonteDeRegrasFalhaVoltaParaOPadraoOuAUltima(t *testing.T) {
	ctx := context.Background()
	l := &leitorRegrasFalso{err: errors.New("banco fora")}
	f := NewRegrasVendaFonte(l, 30*time.Second, nil)
	if r := f.Regras(ctx, "t1").Resolvida(); r.Faixas != PadraoRegrasVenda().Faixas {
		t.Errorf("sem leitura nenhuma, vale o padrão; veio %+v", r.Faixas)
	}

	l.err = nil
	l.versao = VersaoRegras{ID: "v1", Regras: RegrasVenda{Faixas: FaixasIdade{IdadeParticular: 20}}}
	f.Invalida("t1")
	f.Regras(ctx, "t1")
	l.err = errors.New("banco fora de novo")
	f.Invalida("t1")
	if r := f.Regras(ctx, "t1"); r.Faixas.IdadeParticular != 20 {
		t.Errorf("com o banco fora, vale a última versão lida; veio %+v", r.Faixas)
	}
}

func TestPromptUsaAsRegrasDaConfiguracao(t *testing.T) {
	cfg := TenantConfig{RegrasVenda: RegrasVenda{Textos: map[ParteRegra]string{PartePorQue: "- MOTIVO ESCRITO NA TELA"}}}
	p := BuildPrompt(cfg, ConversationContext{}, "oi", time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC))
	if !strings.Contains(p, "MOTIVO ESCRITO NA TELA") {
		t.Error("o prompt deveria usar as regras que vieram na configuração")
	}
}
