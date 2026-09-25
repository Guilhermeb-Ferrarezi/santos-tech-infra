package main

import "strings"

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
