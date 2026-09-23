package main

import (
	"encoding/json"
	"testing"
)

func TestValidRescheduleSource(t *testing.T) {
	cases := map[string]bool{
		"notion":  true,
		"pending": true,
		"":        false,
		"x":       false,
		"NOTION":  false,
	}
	for in, want := range cases {
		if got := validRescheduleSource(in); got != want {
			t.Errorf("validRescheduleSource(%q) = %v, quer %v", in, got, want)
		}
	}
}

// PATCH tem que significar "mude o que eu mandei", não "substitua o registro".
//
// Uma chamada legítima mandando só o systemPrompt zerou, em produção, a base de
// conhecimento inteira, o nome do bot, a allowlist e os números dos admins — e
// o bot parou de responder a todo mundo, sem erro nenhum.
func TestPatchDeConfigSoMexeNoQueVeio(t *testing.T) {
	var body dashConfigPatch
	if err := json.Unmarshal([]byte(`{"systemPrompt":"novo prompt"}`), &body); err != nil {
		t.Fatalf("erro: %v", err)
	}
	if body.SystemPrompt == nil || *body.SystemPrompt != "novo prompt" {
		t.Error("o campo enviado não chegou")
	}
	// Tudo que não veio precisa ser nil — é o nil que vira COALESCE e preserva.
	ausentes := map[string]bool{
		"KBContent":            body.KBContent != nil,
		"BotName":              body.BotName != nil,
		"BotAllowedNumbers":    body.BotAllowedNumbers != nil,
		"AdminWhatsAppNumbers": body.AdminWhatsAppNumbers != nil,
		"BotEnabledByDefault":  body.BotEnabledByDefault != nil,
		"DebounceMs":           body.DebounceMs != nil,
		"AdminSystemPrompt":    body.AdminSystemPrompt != nil,
		"NotifPhone":           body.NotifPhone != nil,
	}
	for campo, veio := range ausentes {
		if veio {
			t.Errorf("%s não foi enviado mas chegou preenchido — seria sobrescrito", campo)
		}
	}
}

// Lista vazia ENVIADA de propósito é diferente de lista ausente: uma esvazia,
// a outra preserva.
func TestListaVaziaEnviadaEsvaziaDeVerdade(t *testing.T) {
	var comLista dashConfigPatch
	if err := json.Unmarshal([]byte(`{"botAllowedNumbers":[]}`), &comLista); err != nil {
		t.Fatalf("erro: %v", err)
	}
	if comLista.BotAllowedNumbers == nil {
		t.Fatal("lista vazia enviada virou ausente")
	}
	if j := jsonbOuNil(comLista.BotAllowedNumbers); j == nil || *j != "[]" {
		t.Errorf("lista vazia deveria virar \"[]\", veio %v", j)
	}

	var semLista dashConfigPatch
	if err := json.Unmarshal([]byte(`{"botName":"Marcos"}`), &semLista); err != nil {
		t.Fatalf("erro: %v", err)
	}
	if jsonbOuNil(semLista.BotAllowedNumbers) != nil {
		t.Error("lista ausente tem que virar nil, para o COALESCE preservar")
	}
}
