package main

import "testing"

// TestDefaultBlogAudienceFallsBackToFamilia garante que o blog público atual
// (santos-tech.com/blog, que nunca manda ?audience=) continua vendo só os
// posts de audience='familia' depois da migração — sem isso a introdução do
// blog de adultos vazaria conteúdo pro público errado por omissão.
func TestDefaultBlogAudienceFallsBackToFamilia(t *testing.T) {
	got, err := defaultBlogAudience("")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if got != "familia" {
		t.Fatalf("esperava default 'familia', veio %q", got)
	}
}

func TestDefaultBlogAudienceAcceptsAdultos(t *testing.T) {
	got, err := defaultBlogAudience("adultos")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if got != "adultos" {
		t.Fatalf("esperava 'adultos', veio %q", got)
	}
}

func TestDefaultBlogAudienceRejectsValorInvalido(t *testing.T) {
	if _, err := defaultBlogAudience("infantil"); err == nil {
		t.Fatal("esperava erro pra audiência inválida, veio nil")
	}
}

// TestValidateBlogCategoryInputDefaultsAudience garante que categorias criadas
// sem o campo novo (ex. clientes antigos do admin ainda sem o seletor) caem em
// 'familia', preservando o comportamento anterior à migração.
func TestValidateBlogCategoryInputDefaultsAudience(t *testing.T) {
	in := BlogCategoryInput{Slug: "novidades", Name: "Novidades"}
	if err := validateBlogCategoryInput(&in); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if in.Audience != "familia" {
		t.Fatalf("esperava default 'familia', veio %q", in.Audience)
	}
}

func TestValidateBlogCategoryInputRejectsAudienciaInvalida(t *testing.T) {
	in := BlogCategoryInput{Slug: "novidades", Name: "Novidades", Audience: "infantil"}
	if err := validateBlogCategoryInput(&in); err == nil {
		t.Fatal("esperava erro pra audiência inválida, veio nil")
	}
}
