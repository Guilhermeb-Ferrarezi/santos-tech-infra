package main

import (
	"encoding/json"
	"strings"
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

func TestValidateSocialPostInput_TituloLongoDemais(t *testing.T) {
	in := baseValidInput()
	in.Title = strings.Repeat("a", 201)
	if err := validateSocialPostInput(&in); err == nil {
		t.Fatal("esperava erro de título longo demais (>200 caracteres)")
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

// --- PUT /social/posts/{id} parcial (mergeSocialPostInput) ---
//
// O PUT deixou de exigir o objeto inteiro: só os campos presentes no JSON
// são aplicados, os ausentes mantêm o valor atual do post (ver
// handleUpdateSocialPost em handlers_social.go). Estes testes cobrem só a
// lógica pura de merge (sem tocar s.db, que é nil no harness de testes).

func baseCurrentPost() *SocialPost {
	return &SocialPost{
		Title:              "Original",
		Caption:            "legenda original",
		Platform:           "instagram",
		Pilar:              "educacional",
		Status:             "ideia",
		Formato:            "carrossel",
		Objetivo:           "alcance",
		Programa:           "create",
		Receita:            "versus",
		MediaURL:           "https://example.com/media.jpg",
		Hashtags:           []string{"#a", "#b"},
		PlataformasDestino: []string{"instagram", "facebook"},
		CopyArte:           json.RawMessage(`[{"slide":1,"headline":"oi"}]`),
	}
}

func TestMergeSocialPostInput_PartialUpdateKeepsOtherFields(t *testing.T) {
	in := socialPostInputFromCurrent(baseCurrentPost())

	raw := map[string]json.RawMessage{
		"title": json.RawMessage(`"Novo título"`),
	}
	if err := mergeSocialPostInput(&in, raw); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	if in.Title != "Novo título" {
		t.Fatalf("title não foi atualizado: %q", in.Title)
	}
	if in.Caption != "legenda original" {
		t.Fatalf("caption não deveria mudar, veio %q", in.Caption)
	}
	if in.Platform != "instagram" || in.Pilar != "educacional" || in.Status != "ideia" {
		t.Fatalf("campo ausente do JSON foi alterado indevidamente: %+v", in)
	}
	if len(in.Hashtags) != 2 || in.Hashtags[0] != "#a" {
		t.Fatalf("hashtags não deveriam mudar: %+v", in.Hashtags)
	}
	if string(in.CopyArte) != `[{"slide":1,"headline":"oi"}]` {
		t.Fatalf("copyArte não deveria mudar: %s", in.CopyArte)
	}
	if len(in.PlataformasDestino) != 2 {
		t.Fatalf("plataformasDestino não deveria mudar: %+v", in.PlataformasDestino)
	}
}

// Campo presente com valor vazio é uma alteração intencional — diferente de
// campo ausente, que preserva o valor atual.
func TestMergeSocialPostInput_PresentEmptyValueOverwrites(t *testing.T) {
	in := socialPostInputFromCurrent(baseCurrentPost())

	raw := map[string]json.RawMessage{
		"caption":  json.RawMessage(`""`),
		"hashtags": json.RawMessage(`[]`),
	}
	if err := mergeSocialPostInput(&in, raw); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if in.Caption != "" {
		t.Fatalf("caption deveria ter sido esvaziada, veio %q", in.Caption)
	}
	if len(in.Hashtags) != 0 {
		t.Fatalf("hashtags deveriam ter sido esvaziadas, veio %+v", in.Hashtags)
	}
	// Título não veio no JSON — continua o mesmo.
	if in.Title != "Original" {
		t.Fatalf("title não deveria ter sido tocado: %q", in.Title)
	}
}

func TestMergeSocialPostInput_TipoInvalidoRetornaErro(t *testing.T) {
	in := socialPostInputFromCurrent(baseCurrentPost())
	raw := map[string]json.RawMessage{
		"responsavelId": json.RawMessage(`"nao-e-numero"`),
	}
	if err := mergeSocialPostInput(&in, raw); err == nil {
		t.Fatal("esperava erro de tipo inválido em responsavelId")
	}
}

// --- Série (social_series) ---

func TestSerieIDOf(t *testing.T) {
	if got := serieIDOf(nil); got != nil {
		t.Fatalf("esperava nil pra post sem série, veio %v", *got)
	}
	got := serieIDOf(&SocialSerieRef{ID: 7, Nome: "Tela&Saúde"})
	if got == nil || *got != 7 {
		t.Fatalf("esperava ponteiro pra 7, veio %v", got)
	}
}

func TestSocialPostInputFromCurrent_PreservaSerie(t *testing.T) {
	current := baseCurrentPost()
	current.Serie = &SocialSerieRef{ID: 3, Nome: "GMN"}
	in := socialPostInputFromCurrent(current)
	if in.SerieID == nil || *in.SerieID != 3 {
		t.Fatalf("esperava serieId=3 reidratado do post atual, veio %v", in.SerieID)
	}
}

func TestMergeSocialPostInput_SerieIdMerge(t *testing.T) {
	in := socialPostInputFromCurrent(baseCurrentPost()) // sem série
	raw := map[string]json.RawMessage{
		"serieId": json.RawMessage(`5`),
	}
	if err := mergeSocialPostInput(&in, raw); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if in.SerieID == nil || *in.SerieID != 5 {
		t.Fatalf("esperava serieId=5 aplicado pelo merge, veio %v", in.SerieID)
	}
}

func TestMergeSocialPostInput_SerieIdNullLimpaSerie(t *testing.T) {
	current := baseCurrentPost()
	current.Serie = &SocialSerieRef{ID: 3, Nome: "GMN"}
	in := socialPostInputFromCurrent(current)
	raw := map[string]json.RawMessage{
		"serieId": json.RawMessage(`null`),
	}
	if err := mergeSocialPostInput(&in, raw); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if in.SerieID != nil {
		t.Fatalf("esperava serieId nil após serieId:null explícito, veio %v", *in.SerieID)
	}
}
