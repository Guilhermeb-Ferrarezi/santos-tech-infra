package main

import "testing"

func TestFingerprintProvider(t *testing.T) {
	cases := map[string]string{
		"sk-p5dY6E8s2aewrwaMGRs57dAnxrBZk":             "OpenAI (formato antigo) ou compatível",
		"sk-proj-abc123456789":                         "OpenAI (chave de projeto)",
		"sk_live_51H8x9K2eZvKYlo2C":                    "Stripe (produção)",
		"vk_test_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789": "Dotfy (sandbox)",
		"abc_dev_2AsUXfMKtwhHwJQTGzsk45uH":             "AbacatePay (sandbox)",
		"ghp_16C7e42F292c6912E7710c838347Ae178B4a":     "GitHub (personal access token)",
		"AKIAIOSFODNN7EXAMPLE":                         "AWS (Access Key ID)",
		"minha_senha_super_secreta_123456":             "",
	}
	for value, want := range cases {
		if got := fingerprintProvider(value); got != want {
			t.Errorf("fingerprintProvider(%q) = %q, esperava %q", value, got, want)
		}
	}
}

func TestFingerprintProviderFamily(t *testing.T) {
	cases := map[string]string{
		"sk-p5dY6E8s2aewrwaMGRs57dAnxrBZk":  "openai",
		"sk_live_51H8x9K2eZvKYlo2C":         "stripe",
		"pk_live_4eC39HqLyjWDarjtT1zdp7dc":  "", // pública, não autentica sozinha
		"vk_test_AbCdEfGhIjKlMnOpQrStUvWx1": "dotfy",
		"abc_dev_2AsUXfMKtwhHwJQTGzsk45uH":  "abacatepay",
		"ghp_16C7e42F292c6912E7710c838347A": "github",
		"AKIAIOSFODNN7EXAMPLE":              "", // precisa do secret pair, não dá pra verificar sozinha
		"xoxb-1234-5678-abcdefg":            "slack",
	}
	for value, want := range cases {
		_, gotFamily := fingerprintProviderFamily(value)
		if gotFamily != want {
			t.Errorf("fingerprintProviderFamily(%q) family = %q, esperava %q", value, gotFamily, want)
		}
	}
}

func TestKeywordFamily(t *testing.T) {
	cases := map[string]string{
		"PAGARME_API_KEY":          "pagarme",
		"MERCADOPAGO_ACCESS_TOKEN": "mercadopago",
		"ASAAS_API_KEY":            "asaas",
		"CLOUDFLARE_API_TOKEN":     "cloudflare",
		"ANTHROPIC_API_KEY":        "anthropic",
		"DOTFY_API_KEY":            "", // não deve confundir com nenhum dos novos
		"STRIPE_API_KEY":           "", // continua só via valor, não por keyword
	}
	for keyword, want := range cases {
		_, gotFamily := keywordFamily(keyword)
		if gotFamily != want {
			t.Errorf("keywordFamily(%q) family = %q, esperava %q", keyword, gotFamily, want)
		}
	}
}

func TestKeywordFamily_DisambiguatesPagarmeFromStripe(t *testing.T) {
	// Pagar.me usa o MESMO prefixo sk_test_/sk_live_ da Stripe — sem o nome
	// da keyword, o fingerprint do valor sozinho ia errar o verificador.
	value := "sk_live_51H8x9K2eZvKYlo2C"
	_, valueFamily := fingerprintProviderFamily(value)
	if valueFamily != "stripe" {
		t.Fatalf("esperava que o valor sozinho parecesse Stripe, veio %q", valueFamily)
	}
	_, kwFamily := keywordFamily("PAGARME_API_KEY")
	if kwFamily != "pagarme" {
		t.Fatalf("esperava que a keyword resolvesse pagarme, veio %q", kwFamily)
	}
}
