package main

import (
	"strings"
	"testing"
)

func hashOf(letter string) string { return strings.Repeat(letter, 64) }

// O cruzamento por nome é o que dá ícone pros cards de PC — e a regra mais
// importante é NÃO chutar: ícone errado num card é pior que placeholder.
func TestMatchOpenAppIconPreferenciaExataDepoisSubconjunto(t *testing.T) {
	programs := []labProgramIconRef{
		{Name: "Unity Hub", Hash: hashOf("a")},
		{Name: "Unity 6000.0.23f1", Hash: hashOf("b")},
		{Name: "Microsoft Visual Studio Code (User)", Hash: hashOf("c")},
		{Name: "Discord", Hash: hashOf("d")},
		{Name: "Google Chrome", Hash: hashOf("e")},
		{Name: "Microsoft 365 - pt-br", Hash: hashOf("f")},
		{Name: "blender", Hash: hashOf("g")},
		{Name: "7-Zip 24.08 (x64)", Hash: hashOf("h")},
	}
	cases := []struct {
		app, want, motivo string
	}{
		{"Unity", hashOf("b"), "versão no nome instalado não atrapalha, e o exato ganha do Unity Hub"},
		{"Unity Hub", hashOf("a"), "duas palavras iguais"},
		{"Visual Studio Code", hashOf("c"), "'Microsoft' e '(User)' sobrando no instalado"},
		{"discord", hashOf("d"), "caixa não importa"},
		{"Google Chrome Beta", hashOf("e"), "programa de 2+ palavras contido no nome da janela"},
		{"Blender", hashOf("g"), "instalado em minúsculas"},
		{"7-Zip", hashOf("h"), "arquitetura e versão descartadas"},
		{"Microsoft Edge", "", "uma palavra solta ('microsoft') não pode casar com o 365"},
		{"Zen", "", "nada parecido no inventário"},
		{"VS", "", "nome curtíssimo só casa exato"},
		{"", "", "vazio"},
	}
	for _, c := range cases {
		if got := matchOpenAppIcon(c.app, programs); got != c.want {
			t.Errorf("%q: esperava %q (%s), veio %q", c.app, c.want, c.motivo, got)
		}
	}
}

func TestMatchOpenAppIconDesempataPorNome(t *testing.T) {
	programs := []labProgramIconRef{
		{Name: "Roblox Studio for guibf", Hash: hashOf("a")},
		{Name: "Roblox Player for guibf", Hash: hashOf("b")},
	}
	// Os dois casam igual ("roblox" ⊂ ambos, mesma sobra) — o primeiro em ordem
	// alfabética ganha, sempre o mesmo entre uma requisição e outra.
	if got := matchOpenAppIcon("Roblox", programs); got != hashOf("b") {
		t.Errorf("esperava o Player (alfabeticamente antes), veio %q", got)
	}
}

func TestResolveOpenAppIconsNaoTocaOBancoSemAppsAbertos(t *testing.T) {
	// s.db é nil no harness: se a função consultasse, dava panic. Sem app
	// aberto não há nada a cruzar; e o mapa nil de um scan antigo vira {} pra o
	// JSON nunca sair null.
	s := &Server{}
	devices := []LabDevice{
		{ID: "dev-1"}, // nada aberto, OpenAppIcons nil
	}
	if err := s.resolveOpenAppIcons(t.Context(), devices); err != nil {
		t.Fatal(err)
	}
	if devices[0].OpenAppIcons == nil {
		t.Error("mapa nil deve virar {} pra o JSON nunca sair null")
	}
}
