package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseModelReplyEmptyBubblesIsSilent(t *testing.T) {
	// Cliente só disse "ok": o LLM retorna bubbles vazio → não responde, sem erro.
	out, err := ParseModelReply(`{"bubbles":[],"answered":true,"answeredFromKb":false}`)
	if err != nil {
		t.Fatalf("bubbles vazio não deveria dar erro: %v", err)
	}
	if len(out.Bubbles) != 0 {
		t.Fatalf("esperava 0 balões, veio %d", len(out.Bubbles))
	}
}

func TestParseModelReplyFiltersBlankBubbles(t *testing.T) {
	out, err := ParseModelReply(`{"bubbles":["oi"," ",""],"answered":true,"answeredFromKb":false}`)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if len(out.Bubbles) != 1 || out.Bubbles[0] != "oi" {
		t.Fatalf("esperava só [\"oi\"], veio %#v", out.Bubbles)
	}
}

func TestParseModelReplyMissingAnsweredFails(t *testing.T) {
	if _, err := ParseModelReply(`{"bubbles":["oi"]}`); err == nil {
		t.Fatal("faltando answered deveria dar erro")
	}
}

// O sinal de cancelamento tem que atravessar o parser — sem isso o cliente diz
// "não vou conseguir ir" e o horário fica ocupado para sempre.
func TestParserLeCancelaAula(t *testing.T) {
	out, err := ParseModelReply(`{"bubbles":["Sem problema!"],"answered":true,
	  "answeredFromKb":false,"cancelaAula":true}`)
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if !out.CancelaAula {
		t.Error("cancelaAula=true não chegou no ResponderOutput")
	}

	// Ausente é false — o padrão tem que ser "não cancela".
	out2, err := ParseModelReply(`{"bubbles":["oi"],"answered":true,"answeredFromKb":false}`)
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if out2.CancelaAula {
		t.Error("sem o campo, cancelaAula deveria ser false")
	}
}

func TestPromptPedeCancelaAula(t *testing.T) {
	p := BuildPrompt(TenantConfig{EscolaAbre: "08:00", EscolaFecha: "22:00", AulaDuracaoMin: 60},
		ConversationContext{}, "não vou conseguir ir", time.Unix(1789000000, 0).UTC())
	if !strings.Contains(p, `"cancelaAula"`) {
		t.Error("o schema do prompt não tem cancelaAula")
	}
	if !strings.Contains(p, "JÁ MARCADA") {
		t.Error("o prompt não distingue desmarcar do que já existe de negociar horário")
	}
}
