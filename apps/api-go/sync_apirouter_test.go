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
