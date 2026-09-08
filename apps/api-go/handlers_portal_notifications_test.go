package main

import (
	"strings"
	"testing"
)

func baseValidPortalTemplateInput() portalTemplateInput {
	return portalTemplateInput{
		Nome:             "boas-vindas",
		TituloTemplate:   "Bem-vindo!",
		MensagemTemplate: "Seja bem-vindo à plataforma.",
	}
}

func TestPortalTemplateInput_ValidateOK(t *testing.T) {
	in := baseValidPortalTemplateInput()
	if err := in.validate(); err != nil {
		t.Fatalf("esperava nil, veio %v", err)
	}
}

func TestPortalTemplateInput_ValidateCamposObrigatorios(t *testing.T) {
	cases := []struct {
		name string
		in   portalTemplateInput
	}{
		{"nome vazio", func() portalTemplateInput { in := baseValidPortalTemplateInput(); in.Nome = ""; return in }()},
		{"nome longo demais", func() portalTemplateInput {
			in := baseValidPortalTemplateInput()
			in.Nome = strings.Repeat("a", 201)
			return in
		}()},
		{"título vazio", func() portalTemplateInput { in := baseValidPortalTemplateInput(); in.TituloTemplate = ""; return in }()},
		{"título longo demais", func() portalTemplateInput {
			in := baseValidPortalTemplateInput()
			in.TituloTemplate = strings.Repeat("a", 201)
			return in
		}()},
		{"mensagem vazia", func() portalTemplateInput { in := baseValidPortalTemplateInput(); in.MensagemTemplate = ""; return in }()},
		{"mensagem longa demais", func() portalTemplateInput {
			in := baseValidPortalTemplateInput()
			in.MensagemTemplate = strings.Repeat("a", 4001)
			return in
		}()},
	}
	for _, tc := range cases {
		if err := tc.in.validate(); err == nil {
			t.Fatalf("%s: esperava erro", tc.name)
		}
	}
}
