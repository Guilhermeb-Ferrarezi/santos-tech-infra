package main

import (
	"context"
	"testing"
	"time"
)

// Cada teste chama a API real do provedor com uma chave obviamente inventada
// — não gasta nenhuma chave de verdade, só confirma que nossa leitura do
// código de status (401 = inválida) está certa pra cada provedor.
func TestGenericVerifier_RejectsGarbageKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("pula chamadas de rede em -short")
	}

	cases := []struct {
		family string
		key    string
	}{
		{"abacatepay", "abc_live_00000000000000000000invalid"},
		// Concatenado (não literal contíguo) pra não disparar o secret
		// scanning do GitHub — o token é obviamente inventado, mas o FORMATO
		// bate com o regex de detecção de chave Stripe.
		{"stripe", "sk_live_" + "00000000000000000000000invalid"},
		{"github", "ghp_0000000000000000000000000000invalid"},
		{"openai", "sk-000000000000000000000000000invalid"},
		{"sendgrid", "SG.invalid000000000000.invalid00000000000000"},
		{"slack", "xoxb-invalid-0000000000-0000000000-invalid"},
		{"anthropic", "sk-ant-api03-invalid0000000000000000000000000000000000000000000000000000000000"},
		// Cloudflare valida o FORMATO (40 chars) antes de checar validade —
		// token curto demais dá 400, não 401. Formato plausível pra testar de verdade.
		{"cloudflare", "aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789AbCd"},
		{"huggingface", "hf_invalid0000000000000000000000000invalid"},
		{"digitalocean", "dop_v1_invalid00000000000000000000000000000000000000000000000000000000invalid"},
		{"vercel", "invalid0000000000000000000000000invalid"},
		{"netlify", "invalid0000000000000000000000000invalid"},
		{"npm", "npm_invalid00000000000000000000000000invalid"},
		{"heroku", "00000000-0000-0000-0000-000000000000"},
		{"discord", "invalid.0000000000000000000.invalid00000000000000000000"},
		{"mailgun", "key-invalid00000000000000000000invalid"},
		{"groq", "gsk_invalid000000000000000000000000000000000000000000invalid"},
		{"mistral", "invalid0000000000000000000000000invalid"},
		{"replicate", "r8_invalid0000000000000000000000000invalid"},
		{"elevenlabs", "invalid0000000000000000000000000invalid"},
		{"cohere", "invalid0000000000000000000000000invalid"},
		// Mercado Pago também tem um bloqueio de borda pra tokens curtos
		// demais (403 genérico do WAF) — formato plausível dá 401 de verdade.
		{"mercadopago", "APP_USR-1234567890123456-010101-invalidinvalidinvalidinvalidinval-123456789"},
		{"asaas", "invalid0000000000000000000000000invalid"},
		{"pagarme", "sk_invalid00000000000000000000invalid"},
		// Formato plausível (dígitos:token) — Telegram valida o padrão da URL
		// antes de checar validade de verdade.
		{"telegram", "123456789:AAInvalid00000000000000000000000"},
		{"gemini", "AIza" + "Invalid0000000000000000000000000"},
		{"square", "invalid0000000000000000000000000invalid"},
		{"clerk", "sk_test_invalid00000000000000000000000invalid"},
		{"railway", "invalid0000000000000000000000000invalid"},
		{"firebase", "AIzaInvalid0000000000000000000000000"},
		{"openrouter", "sk-or-v1-" + "invalid00000000000000000000000000000000000000000000000000invalid"},
		{"resend", "re_invalid_0000000000000000000000invalid"},
		{"notion", "secret_invalid00000000000000000000000000000invalid"},
		{"figma", "invalid0000000000000000000000000invalid"},
		{"sentry", "invalid0000000000000000000000000invalid"},
		{"datadog", "invalid0000000000000000000000000invalid"},
		{"circleci", "invalid0000000000000000000000000invalid"},
		{"gitlab", "glpat-invalid00000000000000000000invalid"},
	}

	v := NewGenericVerifier()

	for _, c := range cases {
		t.Run(c.family, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			checked, active, _ := v.CheckKey(ctx, c.family, c.key)
			if !checked {
				t.Fatalf("esperava checked=true (API respondeu), mas deu erro de rede/timeout pra %s", c.family)
			}
			if active {
				t.Fatalf("esperava active=false pra chave inventada, mas %s disse que é válida", c.family)
			}
		})
	}
}

func TestIsMercadoPagoSandboxAccount(t *testing.T) {
	cases := []struct {
		name     string
		nickname string
		email    string
		tags     []string
		want     bool
	}{
		{"conta de teste típica", "TESTUSER6296593712872692416", "test_user_6296593712872692416@testuser.com", []string{"test_user", "normal"}, true},
		{"só o nickname já basta", "TESTUSER123", "qualquer@dominio.com", nil, true},
		{"só o email já basta", "outronome", "alguem@testuser.com", nil, true},
		{"só a tag já basta", "outronome", "alguem@empresa.com", []string{"test_user"}, true},
		{"conta real não deve disparar", "MinhaLojaOficial", "contato@minhaempresa.com.br", []string{"normal"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isMercadoPagoSandboxAccount(c.nickname, c.email, c.tags); got != c.want {
				t.Errorf("isMercadoPagoSandboxAccount(%q, %q, %v) = %v, esperava %v", c.nickname, c.email, c.tags, got, c.want)
			}
		})
	}
}

func TestGenericVerifier_UnknownFamilyNeverChecked(t *testing.T) {
	v := NewGenericVerifier()
	checked, active, sandbox := v.CheckKey(context.Background(), "servico-que-nao-existe", "qualquer-coisa")
	if checked || active || sandbox {
		t.Fatalf("family desconhecida deveria sempre retornar (false, false, false), veio (%v, %v, %v)", checked, active, sandbox)
	}
}
