package main

// Testes do sync do roteador de APIs com o secrets-go — só as partes puras
// (catálogo de providers, rótulo de chave), sem Postgres no CI.

import (
	"net/http"
	"testing"
)

func TestSyncProviderDefaults(t *testing.T) {
	// famílias conhecidas devolvem configuração utilizável
	for _, fam := range []string{"openai", "anthropic", "groq", "github", "mercadopago", "dotfy"} {
		d, ok := syncProviderDefaultsFor(fam)
		if !ok {
			t.Fatalf("syncProviderDefaultsFor(%q) deveria existir", fam)
		}
		if d.Name == "" || d.BaseURL == "" || d.TestPath == "" {
			t.Errorf("syncProviderDefaultsFor(%q): campo obrigatório vazio: %+v", fam, d)
		}
		if len(d.UnauthorizedCodes) == 0 || len(d.NoCreditCodes) == 0 {
			t.Errorf("syncProviderDefaultsFor(%q): códigos de failover vazios", fam)
		}
	}

	// família desconhecida → ok=false (não cria provider com config errada)
	if _, ok := syncProviderDefaultsFor("não-existe"); ok {
		t.Error("family desconhecida deveria devolver ok=false")
	}

	// padrão do roteador: Authorization Bearer
	d, _ := syncProviderDefaultsFor("openai")
	if d.AuthHeader != "Authorization" || d.AuthScheme != "Bearer" || d.TestMethod != http.MethodGet {
		t.Errorf("openai: %+v", d)
	}

	// header próprio: Anthropic usa x-api-key, sem prefixo
	d2, _ := syncProviderDefaultsFor("anthropic")
	if d2.AuthHeader != "x-api-key" || d2.AuthScheme != "" {
		t.Errorf("anthropic: %+v", d2)
	}

	// todo provider de IA do catálogo tem adapter de chat mapeado; famílias
	// não-chat não precisam, mas o openai_compatible default não pode estar vazio
	for _, fam := range []string{"openai", "groq", "mistral", "deepseek", "openrouter", "together", "fireworks", "xai", "cohere"} {
		d, ok := syncProviderDefaultsFor(fam)
		if !ok {
			t.Fatalf("syncProviderDefaultsFor(%q) deveria existir", fam)
		}
		if d.ChatAdapter == "" {
			t.Errorf("%s: chatAdapter vazio", fam)
		}
	}
	if d, _ := syncProviderDefaultsFor("anthropic"); d.ChatAdapter != chatAdapterAnthropic {
		t.Errorf("anthropic chatAdapter = %q, queria %q", d.ChatAdapter, chatAdapterAnthropic)
	}
	if d, _ := syncProviderDefaultsFor("cohere"); d.ChatAdapter != chatAdapterCohere {
		t.Errorf("cohere chatAdapter = %q, queria %q", d.ChatAdapter, chatAdapterCohere)
	}
	if d, _ := syncProviderDefaultsFor("openai"); d.ChatAdapter != chatAdapterOpenAICompatible {
		t.Errorf("openai chatAdapter = %q, queria %q", d.ChatAdapter, chatAdapterOpenAICompatible)
	}

	// todo provider de IA tem path e modelo de chat que existem de verdade
	// (ex.: Mistral não tem gpt-4o-mini, OpenRouter não é /v1/chat/completions)
	for _, fam := range []string{"openai", "anthropic", "groq", "mistral", "cohere", "deepseek", "openrouter", "together", "fireworks", "xai"} {
		d, ok := syncProviderDefaultsFor(fam)
		if !ok {
			t.Fatalf("syncProviderDefaultsFor(%q) deveria existir", fam)
		}
		if d.ChatPath == "" {
			t.Errorf("%s: chatPath vazio", fam)
		}
		if d.ChatModel == "" {
			t.Errorf("%s: chatModel vazio", fam)
		}
	}
	if d, _ := syncProviderDefaultsFor("openrouter"); d.ChatPath != "/api/v1/chat/completions" {
		t.Errorf("openrouter chatPath = %q, queria /api/v1/chat/completions", d.ChatPath)
	}
	if d, _ := syncProviderDefaultsFor("cohere"); d.BaseURL != "https://api.cohere.com" {
		t.Errorf("cohere baseURL = %q, queria https://api.cohere.com", d.BaseURL)
	}

	// Discord usa prefixo "Bot", não "Bearer"
	d3, _ := syncProviderDefaultsFor("discord")
	if d3.AuthScheme != "Bot" {
		t.Errorf("discord: %+v", d3)
	}

	// Slack testa via POST (auth.test não aceita GET)
	d4, _ := syncProviderDefaultsFor("slack")
	if d4.TestMethod != http.MethodPost {
		t.Errorf("slack test method: %+v", d4)
	}
}

func TestSyncKeyLabel(t *testing.T) {
	got := syncKeyLabel(secretsSyncHit{RepoFullName: "santos-techrp/api-go", Keyword: "OPENAI_API_KEY"})
	if got != "scan: santos-techrp/api-go (OPENAI_API_KEY)" {
		t.Errorf("label = %q", got)
	}

	// sem keyword (raro, mas não quebra)
	got2 := syncKeyLabel(secretsSyncHit{RepoFullName: "a/b"})
	if got2 != "scan: a/b" {
		t.Errorf("label sem keyword = %q", got2)
	}

	// repo gigante não vaza o resto
	long := "repositorio/" + string(make([]byte, 0))
	for i := 0; i < 200; i++ {
		long += "x"
	}
	got3 := syncKeyLabel(secretsSyncHit{RepoFullName: long, Keyword: "K"})
	if len(got3) > 160 {
		t.Errorf("label não foi truncada: %d", len(got3))
	}
}
