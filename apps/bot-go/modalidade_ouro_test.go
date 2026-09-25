package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Teste de ouro do bloco "Turma ou curso particular".
//
// As regras de venda estão saindo de strings no código para textos editáveis
// na tela (spec 2026-09-25-bot-regras-venda-editaveis). É nessa mudança que
// uma frase pode sumir sem ninguém ver: o bot continua respondendo, só que sem
// a regra. O ouro congela o texto que o bot recebe hoje, idade por idade, e
// qualquer diferença falha o teste até alguém olhar e regravar de propósito:
//
//	go test -run TestModalidadeOuro -atualiza-ouro

var atualizaOuro = flag.Bool("atualiza-ouro", false, "regrava testdata/modalidade_ouro")

// idadesDoOuro cobre cada faixa e as bordas entre elas.
var idadesDoOuro = []int{0, 12, 14, 15, 16, 17, 45}

func TestModalidadeOuro(t *testing.T) {
	for _, idade := range idadesDoOuro {
		t.Run(fmt.Sprintf("%d_anos", idade), func(t *testing.T) {
			obtido := Qualificacao{AlunoIdade: idade}.BlocoDaModalidade()
			arquivo := filepath.Join("testdata", "modalidade_ouro", fmt.Sprintf("%d.txt", idade))

			if *atualizaOuro {
				if err := os.MkdirAll(filepath.Dir(arquivo), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(arquivo, []byte(obtido), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}

			bruto, err := os.ReadFile(arquivo)
			if err != nil {
				t.Fatalf("ouro ausente (rode com -atualiza-ouro): %v", err)
			}
			// O git no Windows (core.autocrlf) devolve o arquivo com CRLF; o
			// prompt é montado com LF. A comparação é do texto, não do fim de linha.
			esperado := strings.ReplaceAll(string(bruto), "\r\n", "\n")
			if obtido != esperado {
				t.Errorf("o bloco mudou para %d anos. Se foi de propósito, confira e regrave com -atualiza-ouro.\n%s",
					idade, diffDeLinhas(esperado, obtido))
			}
		})
	}
}

// diffDeLinhas lista as linhas que só existem de um dos lados. Não é um diff
// de verdade — é o bastante para achar a frase que sumiu ou apareceu.
func diffDeLinhas(esperado, obtido string) string {
	conta := func(s string) map[string]int {
		m := map[string]int{}
		for _, l := range strings.Split(s, "\n") {
			m[l]++
		}
		return m
	}
	e, o := conta(esperado), conta(obtido)
	var b strings.Builder
	for _, l := range strings.Split(esperado, "\n") {
		if e[l] > o[l] {
			fmt.Fprintf(&b, "- %s\n", l)
			e[l]--
		}
	}
	e = conta(esperado)
	for _, l := range strings.Split(obtido, "\n") {
		if o[l] > e[l] {
			fmt.Fprintf(&b, "+ %s\n", l)
			o[l]--
		}
	}
	return b.String()
}
