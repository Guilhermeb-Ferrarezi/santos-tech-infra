package main

import (
	"strings"
	"testing"
	"time"
)

// Modo observador (fase 4 do follow-up): com um humano atendendo, o bot não
// responde, mas lê a mensagem do cliente para não perder "me chama em dezembro"
// nem "vou falar com meu marido e te aviso".

func TestDeveObservar(t *testing.T) {
	for texto, want := range map[string]bool{
		"ok":                               false,
		"obrigada!":                        false,
		"tá bom então":                     true,
		"me chama em dezembro":             true,
		"  ":                               false,
		"vou falar com meu marido e aviso": true,
	} {
		if got := deveObservar(texto); got != want {
			t.Errorf("deveObservar(%q) = %v, queria %v", texto, got, want)
		}
	}
}

func TestPromptDoObservador(t *testing.T) {
	hoje := time.Date(2026, 9, 25, 15, 0, 0, 0, brLocation)
	p := promptDoObservador("em dezembro consigo mais", "Fez a experimental.", hoje)
	for _, esperado := range []string{"2026-09-25", "sexta-feira", "em dezembro consigo mais", "Fez a experimental.", `"retorno"`, `"compromisso"`, "NÃO responda"} {
		if !strings.Contains(p, esperado) {
			t.Errorf("prompt sem %q:\n%s", esperado, p)
		}
	}
}

func TestParseObservacao(t *testing.T) {
	o, ok := parseObservacao("Claro! ```json\n{\"retorno\":{\"data\":\"2026-12-01\",\"confianca\":0.5},\"compromisso\":null}\n```")
	if !ok || o.Retorno == nil || o.Retorno.Data != "2026-12-01" || o.Retorno.Confianca != 0.5 || o.Compromisso != "" {
		t.Errorf("parse com texto em volta: %+v ok=%v", o, ok)
	}
	o, ok = parseObservacao(`{"retorno":null,"compromisso":"  vai falar com o marido  "}`)
	if !ok || o.Retorno != nil || o.Compromisso != "vai falar com o marido" {
		t.Errorf("parse compromisso: %+v", o)
	}
	for _, ruim := range []string{"", "não sei", "{quebrado", `{"retorno":"amanhã"}`} {
		if _, ok := parseObservacao(ruim); ok {
			t.Errorf("parseObservacao(%q) deveria falhar", ruim)
		}
	}
}

func TestDataDoRetornoObservado(t *testing.T) {
	hoje := time.Date(2026, 9, 25, 23, 30, 0, 0, brLocation)
	casos := map[string]bool{
		"2026-09-26": true,  // amanhã
		"2026-09-25": false, // hoje: já está falando com alguém
		"2026-09-01": false, // passado
		"2027-10-29": true,  // até 400 dias
		"2027-11-01": false, // longe demais: data inventada
		"26/09/2026": false,
		"":           false,
	}
	for in, want := range casos {
		if _, ok := dataDoRetornoObservado(in, hoje); ok != want {
			t.Errorf("dataDoRetornoObservado(%q) ok=%v, queria %v", in, ok, want)
		}
	}
}

func TestLinhaDeCompromisso(t *testing.T) {
	hoje := time.Date(2026, 9, 25, 10, 0, 0, 0, brLocation)
	if got := linhaDeCompromisso("  vai   falar com\no marido ", hoje); got != "Compromisso (25/09): vai falar com o marido" {
		t.Errorf("linha = %q", got)
	}
	if got := linhaDeCompromisso(strings.Repeat("a", 500), hoje); len([]rune(got)) > 200 {
		t.Errorf("linha longa demais: %d", len([]rune(got)))
	}
	if linhaDeCompromisso("   ", hoje) != "" {
		t.Error("compromisso vazio virou linha")
	}
}

// Compromisso registrado pelo observador tem que chegar ao bot quando ele
// voltar a atender — senão pergunta de novo o que a pessoa já disse.
func TestBlocoDoDossieMostraCompromissos(t *testing.T) {
	q := Qualificacao{Compromissos: "Compromisso (25/09): vai falar com o marido\nCompromisso (26/09): vai ver a agenda do filho"}
	b := q.BlocoDoDossie()
	if strings.Contains(b, "Nada ainda") || !strings.Contains(b, "vai falar com o marido") || !strings.Contains(b, "vai ver a agenda do filho") {
		t.Errorf("dossiê sem os compromissos:\n%s", b)
	}
}
