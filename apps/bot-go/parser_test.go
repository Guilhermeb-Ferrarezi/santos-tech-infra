package main

import "testing"

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
