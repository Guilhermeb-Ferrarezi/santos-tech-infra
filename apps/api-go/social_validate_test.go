package main

import (
	"encoding/json"
	"testing"
)

func baseValidInput() SocialPostInput {
	return SocialPostInput{
		Title: "x", Platform: "instagram", Pilar: "educacional", Status: "ideia",
		Formato: "carrossel", Objetivo: "alcance", Programa: "create", Receita: "versus",
		CopyArte: json.RawMessage(`[{"slide":1,"headline":"oi"}]`),
	}
}

func TestValidateSocialPostInput_OK(t *testing.T) {
	in := baseValidInput()
	if err := validateSocialPostInput(&in); err != nil {
		t.Fatalf("esperava nil, veio %v", err)
	}
}

func TestValidateSocialPostInput_FormatoInvalido(t *testing.T) {
	in := baseValidInput()
	in.Formato = "tiktok_dance"
	if err := validateSocialPostInput(&in); err == nil {
		t.Fatal("esperava erro de formato inválido")
	}
}

func TestValidateSocialPostInput_ObjetivoInvalido(t *testing.T) {
	in := baseValidInput()
	in.Objetivo = "viralizar"
	if err := validateSocialPostInput(&in); err == nil {
		t.Fatal("esperava erro de objetivo inválido")
	}
}

func TestValidateSocialPostInput_ProgramaInvalido(t *testing.T) {
	in := baseValidInput()
	in.Programa = "bootcamp"
	if err := validateSocialPostInput(&in); err == nil {
		t.Fatal("esperava erro de programa inválido")
	}
}

func TestValidateSocialPostInput_ReceitaInvalida(t *testing.T) {
	in := baseValidInput()
	in.Receita = "colagem"
	if err := validateSocialPostInput(&in); err == nil {
		t.Fatal("esperava erro de receita inválida")
	}
}

func TestValidateSocialPostInput_PlataformaDestinoInvalida(t *testing.T) {
	in := baseValidInput()
	in.PlataformasDestino = []string{"instagram", "orkut"}
	if err := validateSocialPostInput(&in); err == nil {
		t.Fatal("esperava erro de plataforma-destino inválida")
	}
}

func TestValidateSocialPostInput_ProgramaVazioOK(t *testing.T) {
	in := baseValidInput()
	in.Programa = ""
	in.Receita = ""
	if err := validateSocialPostInput(&in); err != nil {
		t.Fatalf("programa/receita vazios devem ser válidos, veio %v", err)
	}
}
