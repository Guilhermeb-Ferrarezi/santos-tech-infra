package main

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Playbook de raciocínio de venda (spec 2026-09-25-bot-playbook-venda, no repo
// dashboard). Visão do Henrique (25/09/2026): o bot sabe O QUE perguntar
// (qualificação) e QUE formato indicar (turma × particular), mas não sabe o
// que fazer com a resposta. "Venda é mais perguntar do que falar"; valor desde
// a primeira mensagem, preço por último.
//
// Duas camadas:
//   - PRINCÍPIOS — poucos, valem em toda conversa. Texto editável na tela, no
//     mesmo documento das regras de venda (PartePrincipios).
//   - SITUAÇÕES — fichas que valem em certos casos, escolhidas aqui em Go pelo
//     dossiê da pessoa.

// BlocoComoVender escreve o bloco "# Como vender" do prompt.
func BlocoComoVender(regras RegrasVenda) string {
	r := regras.Resolvida()
	var b strings.Builder
	b.WriteString("# Como vender\n")
	b.WriteString("Princípios (valem em toda conversa):\n")
	b.WriteString(r.Textos[PartePrincipios] + "\n\n")
	return b.String()
}

// Tetos das fichas no prompt. Cada mensagem paga o tamanho do prompt; com
// dezenas de fichas, isto cabe sem busca por IA.
const (
	maxSituacoesNoPrompt   = 8
	maxCaracteresSituacoes = 6000
)

// SelecionaSituacoes escolhe as fichas que entram no prompt desta conversa.
//
// Só ativas. Ficha com motivo marcado só entra quando o motivo da pessoa é um
// deles; ficha com "para quem" marcado só entra quando bate. Sem dossiê, só as
// gerais — uma ficha sobre "mãe que quer o filho no mercado" não tem o que
// fazer numa conversa que ainda nem sabe para quem é o curso.
//
// Ordem: casa motivo e para quem → casa motivo → casa para quem → gerais; no
// empate, a alterada mais recentemente.
func SelecionaSituacoes(q Qualificacao, ativas []Situacao) []Situacao {
	type candidata struct {
		s     Situacao
		ponto int
	}
	var cands []candidata
	for _, s := range ativas {
		if s.Estado != "ativa" {
			continue
		}
		ponto := 0
		if len(s.Motivos) > 0 {
			casou := false
			for _, m := range s.Motivos {
				if m == q.MotivacaoTipo && m != "" {
					casou = true
				}
			}
			if !casou {
				continue
			}
			ponto += 2
		}
		if s.ParaQuem != "" {
			if s.ParaQuem != q.ParaQuem {
				continue
			}
			ponto++
		}
		cands = append(cands, candidata{s, ponto})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].ponto != cands[j].ponto {
			return cands[i].ponto > cands[j].ponto
		}
		return cands[i].s.AlteradoEm.After(cands[j].s.AlteradoEm)
	})

	var out []Situacao
	total := 0
	for _, c := range cands {
		if len(out) >= maxSituacoesNoPrompt {
			break
		}
		n := utf8.RuneCountInString(textoDaSituacao(len(out), c.s))
		if total+n > maxCaracteresSituacoes {
			continue // uma menor adiante ainda pode caber
		}
		total += n
		out = append(out, c.s)
	}
	return out
}

// idCurtoDaSituacao — o id que o modelo vê ("s1"…). O uuid não vai ao prompt:
// é longo, custa token e o modelo erra ao copiar.
func idCurtoDaSituacao(i int) string { return fmt.Sprintf("s%d", i+1) }

// textoDaSituacao — como uma ficha aparece no prompt.
func textoDaSituacao(i int, s Situacao) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### [%s] %s\n", idCurtoDaSituacao(i), s.Titulo)
	linha := func(rotulo, v string) {
		if strings.TrimSpace(v) != "" {
			fmt.Fprintf(&b, "%s: %s\n", rotulo, strings.TrimSpace(v))
		}
	}
	linha("Quando reconhecer", s.Sinais)
	linha("O que costuma estar por trás", s.PorTras)
	linha("Como conduzir", s.Conduzir)
	linha("Evite", s.Evitar)
	return b.String()
}
