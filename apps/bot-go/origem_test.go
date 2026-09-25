package main

import (
	"strings"
	"testing"
)

// O bloco `referral` só chega na primeira mensagem. Se o parser não o ler ali,
// a origem do lead some para sempre — não há segunda chance nem consulta depois.
func TestWebhookLeAOrigemDoAnuncio(t *testing.T) {
	body := []byte(`{"object":"whatsapp_business_account","entry":[{"changes":[{"value":{
	  "metadata":{"phone_number_id":"123"},
	  "contacts":[{"profile":{"name":"Ana"},"wa_id":"5516999999999"}],
	  "messages":[{"id":"wamid.1","from":"5516999999999","timestamp":"1700000000","type":"text",
	    "text":{"body":"Oi, vi o anúncio"},
	    "referral":{"source_url":"https://www.instagram.com/p/abc",
	                "source_id":"120210000000000","source_type":"ad",
	                "headline":"Curso de programação para crianças",
	                "ctwa_clid":"xyz"}}]}}]}]}`)

	msgs, err := ParseMetaWebhook(body, "123")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("esperava 1 mensagem, veio %d", len(msgs))
	}
	if msgs[0].Origem != "anuncio_instagram" {
		t.Errorf("rede errada: %q (a source_url é do Instagram)", msgs[0].Origem)
	}
	d := msgs[0].OrigemDetalhe
	if !strings.Contains(d, "Curso de programação") {
		t.Errorf("o detalhe não diz QUAL anúncio: %q", d)
	}
	if !strings.Contains(d, "120210000000000") {
		t.Errorf("o detalhe não leva o id do anúncio: %q", d)
	}
}

// Quem digita o número na mão chega sem referral — e isso não pode virar origem
// inventada. Vazio é a resposta certa: é o que faz o bot perguntar depois.
func TestMensagemComumNaoGanhaOrigem(t *testing.T) {
	body := []byte(`{"object":"whatsapp_business_account","entry":[{"changes":[{"value":{
	  "metadata":{"phone_number_id":"123"},
	  "messages":[{"id":"wamid.2","from":"5516988888888","timestamp":"1700000000",
	    "type":"text","text":{"body":"bom dia"}}]}}]}]}`)

	msgs, err := ParseMetaWebhook(body, "123")
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].Origem != "" || msgs[0].OrigemDetalhe != "" {
		t.Errorf("origem inventada para quem chegou sem anúncio: %q / %q",
			msgs[0].Origem, msgs[0].OrigemDetalhe)
	}
}

func TestRedeDoAnuncioPelaURL(t *testing.T) {
	casos := map[string]string{
		"https://www.instagram.com/p/abc": "anuncio_instagram",
		"https://www.facebook.com/123":    "anuncio_facebook",
		"https://fb.me/abc":               "anuncio_facebook",
		"":                                "anuncio",
		"https://exemplo.com/x":           "anuncio",
	}
	for url, quer := range casos {
		if got := origemDoAnuncio(url); got != quer {
			t.Errorf("%q: quero %q, veio %q", url, quer, got)
		}
	}
}

// O texto pronto do link é a única origem que a escola controla sem pagar
// anúncio. Precisa sobreviver ao cliente editar a mensagem antes de enviar —
// que é o caso comum.
func TestMarcadorSobreviveAEdicaoDoCliente(t *testing.T) {
	marcadores := []MarcadorOrigem{
		{Marcador: "vim pelo site da escola", Origem: "site"},
		{Marcador: "vim pelo Google", Origem: "google"},
		{Marcador: "li no blog", Origem: "blog"},
	}

	casos := []struct {
		texto string
		quer  string
	}{
		{"Vim pelo site da escola, queria saber dos cursos", "site"},
		{"olá! vim pelo SITE DA ESCOLA.", "site"},
		{"Oi, vim pelo Google — vocês têm curso de Excel?", "google"},
		{"bom dia, li no blog sobre robótica", "blog"},
		{"bom dia", ""},
		{"", ""},
	}
	for _, c := range casos {
		got, _ := origemPorMarcador(c.texto, marcadores)
		if got != c.quer {
			t.Errorf("%q: quero %q, veio %q", c.texto, c.quer, got)
		}
	}
}

// Marcador apontando para uma origem fora do vocabulário é configuração errada,
// e tem que ser ignorada — não gravada. Origem inválida no banco contamina todo
// relatório que vier depois.
func TestMarcadorComOrigemInvalidaEIgnorado(t *testing.T) {
	got, _ := origemPorMarcador("vim pelo tiktok", []MarcadorOrigem{
		{Marcador: "vim pelo tiktok", Origem: "tiktok"},
	})
	if got != "" {
		t.Errorf("gravou origem fora do vocabulário: %q", got)
	}
}

// O painel é a porta por onde a configuração errada entra. Origem inválida
// gravada aqui contamina todo relatório que vier depois — e não dá para limpar
// retroativamente, porque ninguém sabe o que a pessoa quis dizer.
func TestLimpaMarcadoresDescartaConfiguracaoErrada(t *testing.T) {
	saida := LimpaMarcadores([]MarcadorOrigem{
		{Marcador: "  vim pelo site  ", Origem: " SITE "}, // passa, normalizado
		{Marcador: "vim pelo tiktok", Origem: "tiktok"},   // origem inexistente
		{Marcador: "   ", Origem: "google"},               // marcador vazio casaria com tudo
		{Marcador: "li no blog", Origem: ""},              // sem origem
		{Marcador: "vim pelo Google", Origem: "google"},   // passa
	})
	if len(saida) != 2 {
		t.Fatalf("esperava 2 marcadores válidos, veio %d: %+v", len(saida), saida)
	}
	if saida[0].Marcador != "vim pelo site" || saida[0].Origem != "site" {
		t.Errorf("não normalizou espaço e caixa: %+v", saida[0])
	}
}

// Lista vazia tem que voltar array, nunca nil: o painel edita uma lista, e nil
// vira `null` no JSON, que quebra o .map na primeira renderização.
func TestLimpaMarcadoresNuncaDevolveNil(t *testing.T) {
	if s := LimpaMarcadores(nil); s == nil {
		t.Error("devolveu nil; o painel recebe null e quebra")
	}
	if s := LimpaMarcadores([]MarcadorOrigem{{Marcador: "x", Origem: "invalida"}}); s == nil || len(s) != 0 {
		t.Errorf("esperava array vazio, veio %v", s)
	}
}

func TestOrigemLegivelNaoInventaDesconhecida(t *testing.T) {
	if s := OrigemLegivel(""); s != "" {
		t.Errorf("origem vazia virou %q; não saber e ter respondido não são a mesma coisa", s)
	}
	if s := OrigemLegivel("anuncio_instagram"); s == "" {
		t.Error("origem válida ficou sem tradução para o painel")
	}
}
