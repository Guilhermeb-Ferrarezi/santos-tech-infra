package main

import (
	"strings"
	"testing"
	"time"
)

// Caso real que originou isto (25/09/2026): a cliente disse "em dezembro
// consigo mais", o bot gravou o retorno e ninguém nunca foi avisado — a fila
// só processava kind=follow_up.

func TestAvisoDeRetornoTemTudoQueOHumanoPrecisa(t *testing.T) {
	p := retornoPendente{
		ConvID:    "conv-1",
		FireAt:    time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
		Frase:     "em dezembro consigo fazer mais",
		Nome:      "Vivian Moraes",
		Telefone:  "5511999990000",
		Resumo:    "Fez a experimental presencial e gostou.",
		Confianca: 0.5,
	}
	txt := avisoDeRetorno(p, "https://santos-tech.com/dashboard")
	for _, esperado := range []string{
		"Vivian Moraes",
		"5511999990000",
		"em dezembro consigo fazer mais",
		"Fez a experimental presencial",
		"https://santos-tech.com/dashboard/admin/whats/conversas?c=conv-1",
		"data aproximada",
	} {
		if !strings.Contains(txt, esperado) {
			t.Errorf("aviso sem %q:\n%s", esperado, txt)
		}
	}
}

func TestAvisoDeRetornoSemNomeNaoQuebra(t *testing.T) {
	txt := avisoDeRetorno(retornoPendente{ConvID: "c", Telefone: "5516"}, "https://x")
	if !strings.Contains(txt, "5516") {
		t.Errorf("sem nome, o aviso deveria identificar pelo telefone:\n%s", txt)
	}
	if strings.Contains(txt, "data aproximada") {
		t.Error("confiança zero (não informada) não deveria marcar data aproximada")
	}
}

// Ao entrar no ar, a fila tem retornos vencidos há semanas. Eles não podem
// virar uma rajada de avisos: os atrasados vão num resumo só.
func TestSeparaAtrasados(t *testing.T) {
	agora := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	ps := []retornoPendente{
		{ID: "hoje", FireAt: agora.Add(-2 * time.Hour)},
		{ID: "ha-6-dias", FireAt: agora.AddDate(0, 0, -6)},
		{ID: "ha-20-dias", FireAt: agora.AddDate(0, 0, -20)},
	}
	noPrazo, atrasados := separaAtrasados(ps, agora)
	if len(noPrazo) != 2 || len(atrasados) != 1 || atrasados[0].ID != "ha-20-dias" {
		t.Fatalf("noPrazo=%v atrasados=%v", noPrazo, atrasados)
	}
}

func TestResumoDeAtrasadosListaTodosNumaMensagem(t *testing.T) {
	at := []retornoPendente{
		{ConvID: "a", Nome: "Ana", FireAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Frase: "me chama em agosto"},
		{ConvID: "b", Telefone: "5516", FireAt: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)},
	}
	txt := resumoDeAtrasados(at, "https://p")
	for _, esperado := range []string{"2 retornos", "Ana", "me chama em agosto", "5516", "01/08", "https://p/admin/whats/conversas?c=b"} {
		if !strings.Contains(txt, esperado) {
			t.Errorf("resumo sem %q:\n%s", esperado, txt)
		}
	}
}

// A frase original do cliente precisa sobreviver até o aviso — antes o
// payload gravava só {"kind":"reactivation"} e a frase se perdia.
func TestPayloadDoRetornoGuardaAFrase(t *testing.T) {
	raw := payloadDoRetorno(&ScheduledContact{RawPhrase: "te chamo dia 10", ResolvedDate: "2026-11-10", Confidence: 0.9})
	for _, esperado := range []string{`"kind":"reactivation"`, `"rawPhrase":"te chamo dia 10"`, `"confidence":0.9`} {
		if !strings.Contains(string(raw), esperado) {
			t.Errorf("payload sem %s: %s", esperado, raw)
		}
	}
}
