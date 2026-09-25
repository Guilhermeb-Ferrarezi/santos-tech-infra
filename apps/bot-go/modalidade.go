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

// idadeAdultoParticular — a partir desta idade, o atendimento é particular.
const idadeAdultoParticular = 17

// idadeFimDaFaixaTurma — a partir desta idade (e até 16), a turma ainda cabe,
// mas a progressão de 4–6 anos não cabe mais inteira.
const idadeFimDaFaixaTurma = 15

// BlocoDaModalidade escreve as regras de turma × particular e, quando a idade
// já é conhecida, a indicação calculada para ESTA pessoa.
func (q Qualificacao) BlocoDaModalidade() string {
	var b strings.Builder
	b.WriteString("# Turma ou curso particular\n")
	b.WriteString("A escola tem duas modalidades, com o MESMO conteúdo e a mesma qualidade — muda só a forma:\n")
	b.WriteString("- TURMA: os programas (Júnior e Create), para crianças e adolescentes até 16 anos, com outros alunos da mesma faixa, em horário fixo. É uma progressão ano a ano.\n")
	b.WriteString("- CURSO PARTICULAR: só o aluno e o professor, para QUALQUER idade (criança, adolescente, adulto, idoso). O aluno escolhe dia, horário e frequência, e começa sem esperar turma.\n")
	b.WriteString("Vocabulário: o nome é CURSO PARTICULAR (ou \"aula particular\"). NUNCA diga \"curso de adulto\", \"curso para adultos\" ou \"aula de adulto\" — mesmo que a Base de Conhecimento use esse termo: lá é só o rótulo antigo do curso particular.\n")
	b.WriteString("Adulto NUNCA vai para turma — não existe turma de adulto.\n\n")

	b.WriteString("Como decidir (nesta ordem):\n")
	b.WriteString("1. Saiba a IDADE do aluno antes de indicar qualquer curso ou formato.\n")
	fmt.Fprintf(&b, "2. %d anos ou mais: curso particular.\n", idadeAdultoParticular)
	fmt.Fprintf(&b, "3. %d–16 anos: cabe em turma, mas é o fim da faixa. Entenda o objetivo antes de indicar.\n", idadeFimDaFaixaTurma)
	fmt.Fprintf(&b, "4. Até %d anos: descubra o interesse com precisão (se vier amplo, como \"programação\", pergunte a direção: jogos, sites, apps...). Confira na Base de Conhecimento se esse conteúdo está dentro de um programa que atende a idade. Está? Indique a TURMA. Não está em programa nenhum? Indique o PARTICULAR.\n", idadeFimDaFaixaTurma-1)
	b.WriteString("5. Mesmo cabendo em turma: se a disponibilidade da pessoa não bate com nenhuma turma existente, o PARTICULAR resolve (início imediato, sob medida). Só afirme que não há turma no horário se a Base de Conhecimento listar os horários das turmas; se não listar, NÃO invente — siga para a aula experimental e a equipe encaixa.\n\n")

	b.WriteString("Por que é assim (raciocínio interno — use para montar uma fala de VALOR, não de preço):\n")
	b.WriteString("- Adulto tem rotina cheia (trabalho, compromissos) e pouca disponibilidade; costuma querer retorno rápido (emprego, crescer no trabalho). No particular ele encaixa até 1 hora por semana, no dia e horário dele, e o conteúdo vai direto ao objetivo dele.\n")
	b.WriteString("- Para criança e adolescente, o programa em turma é o melhor caminho quando o conteúdo cabe nele: a criança aprende o que a família procurou E muito mais, com progressão por anos, convivência e troca com a turma.\n")
	b.WriteString("- NUNCA diga ao cliente os motivos da ESCOLA: que é difícil formar turma, que turma precisa de um mínimo de alunos com agenda batendo, que o marketing se divide entre muitos cursos, ou qualquer conta financeira (tempo de permanência, ticket). Isso orienta você; ao cliente, fale só do que ELE ganha.\n")
	b.WriteString("- Em venda, pergunte mais do que fala: descubra o porquê da pessoa (a dor, o motivo, o momento) e ligue a indicação a ele.\n\n")

	b.WriteString("Para esta pessoa:\n")
	switch idade := q.AlunoIdade; {
	case idade <= 0:
		b.WriteString("Ainda não sei a idade do aluno. NÃO indique turma nem particular antes de saber — a idade é a próxima coisa a descobrir quando o assunto chegar em curso.\n")
	case idade >= idadeAdultoParticular:
		fmt.Fprintf(&b, "O aluno tem %d anos: é público do CURSO PARTICULAR. NÃO ofereça turma.\n", idade)
	case idade >= idadeFimDaFaixaTurma:
		fmt.Fprintf(&b, "O aluno tem %d anos: cabe em turma, mas está no fim da faixa dos programas (a progressão é de 4 a 6 anos). Entenda o objetivo: retorno rápido (emprego, mercado, algo específico) → curso particular; desenvolvimento contínuo com o conteúdo de um programa → turma.\n", idade)
	default:
		fmt.Fprintf(&b, "O aluno tem %d anos: idade de turma. Decida pelo interesse e pela disponibilidade (passos 4 e 5).\n", idade)
	}
	b.WriteString("\n")
	return b.String()
}
