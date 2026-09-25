package main

import (
	"fmt"
	"strings"
)

// Turma ou curso particular — qual formato o bot indica, e por quê.
//
// Regra do Henrique (25/09/2026). A escola tem duas modalidades com o MESMO
// conteúdo e a mesma qualidade; o que muda é a forma:
//
//   - TURMA (programas Júnior e Create): só criança e adolescente, até 16 anos.
//     É uma progressão ano a ano, pensada para 4 a 6 anos de escola.
//   - CURSO PARTICULAR: aluno e professor, qualquer idade. Antes se chamava
//     "curso de adulto" — o nome mudou porque criança e adolescente também
//     compram o mesmo serviço. O bot NUNCA usa o nome antigo.
//
// A ordem da decisão:
//
//  1. IDADE primeiro. Sem ela não se indica formato nenhum. Calculada aqui, em
//     Go: pedir ao modelo que "lembre de considerar a idade" é o que ele esquece.
//     (As idades 17 e 15 abaixo são o PADRÃO; a tela pode mudar — ver
//     regras_venda.go.)
//  2. 17+ é público do particular. Quem tem essa idade costuma querer retorno
//     imediato (emprego, mercado) e tem rotina cheia.
//  3. 15–16: cabe em turma, mas está no fim da faixa — não completa a
//     progressão. O bot conversa mais para entender o objetivo.
//  4. Até 14: decide pelo INTERESSE. Se o que a família procura está dentro de
//     um programa, indica a turma (aprende isso e muito mais, por anos). Se não
//     está em programa nenhum, ou se a disponibilidade não bate com nenhuma
//     turma existente, indica o particular.
//
// Os MOTIVOS internos (turma exige 4–5+ alunos com a agenda batendo; a escola
// tem muitos cursos e o marketing se divide; turma dá anos de aluno, particular
// dá meses) vão para o prompt porque é com eles que o bot constrói uma fala de
// valor — mas marcados como proibidos de repetir ao cliente.

// BlocoDaModalidade escreve o bloco com as regras padrão. Mantido para quem
// ainda não tem as regras da tela em mãos (testes, caminhos sem config).
func (q Qualificacao) BlocoDaModalidade() string {
	return q.BlocoDaModalidadeCom(RegrasVenda{})
}

// BlocoDaModalidadeCom escreve as regras de turma × particular — o texto vem
// das regras editáveis (vazio = padrão) — e, quando a idade já é conhecida, a
// indicação calculada para ESTA pessoa.
//
// O que é fixo aqui, e por quê:
//   - a descrição das modalidades (é o produto, não opinião de venda);
//   - a linha "Oportunidade de turma nova:" (é o que a equipe procura na
//     ficha do lead — texto que muda de forma some do radar);
//   - as travas de honestidade (TravasDeHonestidade);
//   - a escolha da faixa e do contraste pela idade.
func (q Qualificacao) BlocoDaModalidadeCom(regras RegrasVenda) string {
	r := regras.Resolvida()
	f := r.Faixas
	var b strings.Builder
	b.WriteString("# Turma ou curso particular\n")
	b.WriteString("A escola tem duas modalidades, com o MESMO conteúdo e a mesma qualidade — muda só a forma:\n")
	fmt.Fprintf(&b, "- TURMA: os programas (Júnior e Create), para crianças e adolescentes até %d anos, com outros alunos da mesma faixa, em horário fixo. É uma progressão ano a ano.\n", f.IdadeParticular-1)
	b.WriteString("- CURSO PARTICULAR: só o aluno e o professor, para QUALQUER idade (criança, adolescente, adulto, idoso). O aluno escolhe dia, horário e frequência, e começa sem esperar turma.\n\n")

	// Vocabulário: cada termo numa linha com o mesmo prefixo — é a única linha
	// do prompt onde a palavra proibida pode aparecer (ver o teste).
	b.WriteString("Vocabulário — o nome é CURSO PARTICULAR (ou \"aula particular\"). Vale mesmo que a Base de Conhecimento use o termo antigo: lá ele é só um rótulo velho.\n")
	for _, t := range r.Vocabulario {
		if t.Usar != "" {
			fmt.Fprintf(&b, "%s%s\": use \"%s\".\n", prefixoLinhaVocabulario, t.Evitar, t.Usar)
		} else {
			fmt.Fprintf(&b, "%s%s\".\n", prefixoLinhaVocabulario, t.Evitar)
		}
	}
	b.WriteString("\n")

	b.WriteString("Faixas de idade:\n")
	for _, parte := range []ParteRegra{ParteFaixaParticular, ParteFaixaFimTurma, ParteFaixaTurma} {
		fmt.Fprintf(&b, "- %s: %s\n", f.rotulo(parte), r.Textos[parte])
	}
	b.WriteString("\n")

	b.WriteString("Como decidir (nesta ordem):\n")
	b.WriteString(r.Textos[ParteDecidir] + "\n\n")

	// Turma nova (Henrique, 25/09/2026): abrir turma é decisão humana. O bot
	// enxerga a oportunidade, não promete, e deixa a linha na ficha do lead.
	b.WriteString("Turma nova: se nenhuma turma serve mas o horário da pessoa parece livre na escola, NÃO prometa abrir turma. Registre em \"qualificacao.observacoes\" a linha \"Oportunidade de turma nova: <dia e horário> — <curso>\" (é por ela que a equipe fica sabendo, na ficha do lead no CRM). Diga que vai passar a possibilidade para a equipe avaliar, e ofereça o particular ou a experimental enquanto isso.\n\n")

	// Ancoragem e contraste. O quanto se pode contrastar com a turma depende
	// de a turma ser ou não produto NOSSO para esta pessoa (Henrique,
	// 25/09/2026): na faixa do particular a alternativa real dela é uma turma
	// em OUTRA escola; abaixo disso, diminuir a turma é agredir o que a
	// própria escola vende.
	faixa := f.faixaDe(q.AlunoIdade)
	b.WriteString("Como apresentar o curso particular (sempre que ele for a indicação):\n")
	b.WriteString(r.Textos[ParteApresentarParticular] + "\n")
	if faixa == ParteFaixaParticular {
		b.WriteString(r.Textos[ParteContrasteSemTurma] + "\n\n")
	} else {
		b.WriteString(r.Textos[ParteContrasteComTurma] + "\n\n")
	}

	b.WriteString("Sempre valem (regras fixas):\n")
	for _, trava := range TravasDeHonestidade {
		b.WriteString("- " + trava + "\n")
	}
	b.WriteString("\n")

	b.WriteString("Por que é assim (raciocínio interno — use para montar uma fala de VALOR, não de preço):\n")
	b.WriteString(r.Textos[PartePorQue] + "\n\n")

	b.WriteString("Para esta pessoa:\n")
	if faixa == ParteFaixaDesconhecida {
		b.WriteString(r.Textos[faixa] + "\n")
	} else {
		fmt.Fprintf(&b, "O aluno tem %d anos (faixa \"%s\"): %s\n", q.AlunoIdade, f.rotulo(faixa), r.Textos[faixa])
	}
	b.WriteString("\n")
	return b.String()
}
