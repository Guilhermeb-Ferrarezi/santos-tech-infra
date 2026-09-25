package main

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Regras de venda como DADOS, não como strings no código.
//
// Spec 2026-09-25-bot-regras-venda-editaveis (dashboard). Em 25/09/2026 o
// Henrique descobriu que o termo "adulto" estava escrito no código do bot sem
// ele ter como saber nem mudar. Princípio dele: tudo que se ajusta pelo
// terminal tem que ter controle na interface.
//
// Por isso o TEXTO das regras (o raciocínio de venda dele) vive aqui como
// padrão e pode ser reescrito pela tela "Como o bot vende". O que continua no
// código é a MECÂNICA: calcular a idade, escolher a faixa e o contraste, a
// linha da oportunidade de turma (que a equipe procura na ficha) e as travas
// de honestidade — que aparecem na tela, mas não são editáveis.
//
// VAZIO = PADRÃO. Parte vazia, faixa zerada ou vocabulário vazio usam o que
// está aqui. E o que é salvo igual ao padrão é gravado vazio (SemPadrao): é
// isso que deixa uma melhoria do padrão chegar a quem nunca personalizou.

// ParteRegra — um pedaço de texto editável das regras.
type ParteRegra string

const (
	ParteFaixaDesconhecida    ParteRegra = "faixaDesconhecida"
	ParteFaixaParticular      ParteRegra = "faixaParticular"
	ParteFaixaFimTurma        ParteRegra = "faixaFimTurma"
	ParteFaixaTurma           ParteRegra = "faixaTurma"
	ParteDecidir              ParteRegra = "decidir"
	ParteApresentarParticular ParteRegra = "apresentarParticular"
	ParteContrasteSemTurma    ParteRegra = "contrasteSemTurma"
	ParteContrasteComTurma    ParteRegra = "contrasteComTurma"
	PartePorQue               ParteRegra = "porQue"
)

// PartesDasRegras — todas as partes, na ordem em que a tela mostra.
var PartesDasRegras = []ParteRegra{
	ParteFaixaDesconhecida, ParteFaixaParticular, ParteFaixaFimTurma, ParteFaixaTurma,
	ParteDecidir, ParteApresentarParticular, ParteContrasteSemTurma,
	ParteContrasteComTurma, PartePorQue,
}

// FaixasIdade — as duas idades que decidem turma × particular. Zero = padrão.
type FaixasIdade struct {
	// A partir desta idade, o atendimento é particular (não existe turma).
	IdadeParticular int `json:"idadeParticular"`
	// A partir desta idade (e abaixo da do particular), a turma ainda cabe,
	// mas a progressão de 4 a 6 anos dos programas não cabe mais inteira.
	IdadeFimFaixaTurma int `json:"idadeFimFaixaTurma"`
}

// TermoVocabulario — uma palavra que o bot não usa, e a que ele usa no lugar.
type TermoVocabulario struct {
	Evitar string `json:"evitar"`
	Usar   string `json:"usar"`
}

// RegrasVenda — o documento editável na tela.
type RegrasVenda struct {
	Faixas      FaixasIdade           `json:"faixas"`
	Textos      map[ParteRegra]string `json:"textos"`
	Vocabulario []TermoVocabulario    `json:"vocabulario"`
}

// Limites. O texto é do admin, não do cliente, mas entra em TODA mensagem do
// bot: o teto protege o tamanho (e o custo) do prompt.
const (
	maxTextoRegra        = 4000
	maxTermosVocabulario = 50
	maxTamanhoTermo      = 80
	idadeMinimaFaixa     = 5
	idadeMaximaFaixa     = 99
)

// prefixoLinhaVocabulario — cada termo evitado vira uma linha que começa
// assim. É a única linha do prompt onde o termo proibido pode aparecer.
const prefixoLinhaVocabulario = "- NÃO escreva \""

// TravasDeHonestidade — sempre valem e NÃO são editáveis (decisão do
// Henrique, 25/09/2026). Tirar uma delas faz o bot inventar ou vazar. A tela
// mostra a lista para ele saber que existe.
var TravasDeHonestidade = []string{
	"Só afirme que não há turma no horário da pessoa se a Base de Conhecimento listar os horários das turmas; se não listar, NÃO invente — siga para a aula experimental e a equipe encaixa.",
	"NUNCA fale mal de uma escola específica e NÃO afirme o que outras escolas oferecem ou deixam de oferecer. Quando comparar, compare com o FORMATO (turma), nunca com um concorrente.",
	"NUNCA diga ao cliente os motivos da ESCOLA: que é difícil formar turma, que turma precisa de um mínimo de alunos com agenda batendo, que o marketing se divide entre muitos cursos, ou qualquer conta financeira (tempo de permanência, ticket). Isso orienta você; ao cliente, fale só do que ELE ganha.",
}

// PadraoRegrasVenda — as regras de 25/09/2026, como o Henrique as ditou (ver
// dashboard/docs/HISTORICO.md §22). É o que vale enquanto nada for editado.
func PadraoRegrasVenda() RegrasVenda {
	return RegrasVenda{
		Faixas: FaixasIdade{IdadeParticular: 17, IdadeFimFaixaTurma: 15},
		Textos: map[ParteRegra]string{
			ParteFaixaDesconhecida: "Ainda não sei a idade do aluno. NÃO indique turma nem particular antes de saber — a idade é a próxima coisa a descobrir quando o assunto chegar em curso.",
			ParteFaixaParticular:   "é público do CURSO PARTICULAR. NÃO ofereça turma: não existe turma para essa idade.",
			ParteFaixaFimTurma:     "cabe em turma, mas está no fim da faixa dos programas (a progressão é de 4 a 6 anos). Entenda o objetivo antes de indicar: retorno rápido (emprego, mercado, algo específico) → curso particular; desenvolvimento contínuo com o conteúdo de um programa → turma.",
			ParteFaixaTurma:        "idade de turma. Descubra o interesse com precisão (se vier amplo, como \"programação\", pergunte a direção: jogos, sites, apps...). Confira na Base de Conhecimento se esse conteúdo está dentro de um programa que atende a idade. Está? Indique a TURMA. Não está em programa nenhum? Indique o PARTICULAR.",
			ParteDecidir: "1. Saiba a IDADE do aluno antes de indicar qualquer curso ou formato. A faixa dela (acima) diz o caminho.\n" +
				"2. Mesmo cabendo em turma: se a disponibilidade da pessoa não bate com nenhuma turma existente, o PARTICULAR resolve (início imediato, sob medida).\n" +
				"3. Se indicar uma turma já em andamento: deixe claro que a turma é recente, que teve poucos meses de aula, e que a reposição de aulas é suficiente para o aluno acompanhar o ponto em que a turma está.",
			ParteApresentarParticular: "- Muita gente imagina, sem perceber, que um bom curso é numa sala com um professor e muitos alunos. Não discuta essa imagem — ANCORE nela: diga que é o MESMO curso, o mesmo conteúdo e a mesma qualidade que a pessoa faria numa turma dividindo a atenção do professor com outros alunos; a diferença é que aqui ela faz sozinha com o professor.\n" +
				"- Depois da âncora, mostre o que muda para ELA: atenção exclusiva; o professor começa do nível que ela já tem, mira no objetivo dela e se adapta ao jeito e ao ritmo dela de aprender, acompanhando a evolução aula a aula.\n" +
				"- NÃO cite número de alunos de turma (nem da escola, nem de outras).",
			ParteContrasteSemTurma: "- Para esta pessoa a turma NÃO é opção aqui — a alternativa real dela é um curso em turma em outro lugar. Então você pode mostrar com franqueza por que a turma serve menos a ela: horário fixo que a rotina dela talvez não comporte, esperar a turma fechar para começar, ritmo e conteúdo pensados para a média da turma, atenção do professor dividida. Faça isso se ela comparar ou hesitar, sem exagero.",
			ParteContrasteComTurma: "- NÃO diminua a turma: ela é o caminho principal para crianças e adolescentes e é produto da escola; compare só para mostrar o ganho do particular.",
			PartePorQue: "- Adulto tem rotina cheia (trabalho, compromissos) e pouca disponibilidade; costuma querer retorno rápido (emprego, crescer no trabalho). No particular ele encaixa até 1 hora por semana, no dia e horário dele, e o conteúdo vai direto ao objetivo dele.\n" +
				"- Para criança e adolescente, o programa em turma é o melhor caminho quando o conteúdo cabe nele: a criança aprende o que a família procurou E muito mais, com progressão por anos, convivência e troca com a turma.\n" +
				"- Em venda, pergunte mais do que fala: descubra o porquê da pessoa (a dor, o motivo, o momento) e ligue a indicação a ele.",
		},
		Vocabulario: []TermoVocabulario{
			{Evitar: "curso de adulto", Usar: "curso particular"},
			{Evitar: "curso para adultos", Usar: "curso particular"},
			{Evitar: "aula de adulto", Usar: "aula particular"},
			{Evitar: "professor ou professora", Usar: "o professor"},
		},
	}
}

// Resolvida preenche com o padrão tudo o que está vazio.
func (r RegrasVenda) Resolvida() RegrasVenda {
	p := PadraoRegrasVenda()
	out := RegrasVenda{Faixas: r.Faixas, Textos: map[ParteRegra]string{}, Vocabulario: r.Vocabulario}
	if out.Faixas.IdadeParticular <= 0 {
		out.Faixas.IdadeParticular = p.Faixas.IdadeParticular
	}
	if out.Faixas.IdadeFimFaixaTurma <= 0 {
		out.Faixas.IdadeFimFaixaTurma = p.Faixas.IdadeFimFaixaTurma
	}
	for _, parte := range PartesDasRegras {
		if t := strings.TrimSpace(r.Textos[parte]); t != "" {
			out.Textos[parte] = t
		} else {
			out.Textos[parte] = p.Textos[parte]
		}
	}
	if len(out.Vocabulario) == 0 {
		out.Vocabulario = p.Vocabulario
	}
	return out
}

// SemPadrao é o que se grava: tudo que é igual ao padrão vira vazio.
func (r RegrasVenda) SemPadrao() RegrasVenda {
	p := PadraoRegrasVenda()
	out := RegrasVenda{Faixas: r.Faixas, Textos: map[ParteRegra]string{}}
	if out.Faixas == p.Faixas {
		out.Faixas = FaixasIdade{}
	}
	for _, parte := range PartesDasRegras {
		t := strings.TrimSpace(r.Textos[parte])
		if t != "" && t != p.Textos[parte] {
			out.Textos[parte] = t
		}
	}
	voc := limpaVocabulario(r.Vocabulario)
	if !mesmoVocabulario(voc, p.Vocabulario) {
		out.Vocabulario = voc
	}
	return out
}

func limpaVocabulario(v []TermoVocabulario) []TermoVocabulario {
	var out []TermoVocabulario
	for _, t := range v {
		out = append(out, TermoVocabulario{Evitar: strings.TrimSpace(t.Evitar), Usar: strings.TrimSpace(t.Usar)})
	}
	return out
}

func mesmoVocabulario(a, b []TermoVocabulario) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Valida confere o documento como veio da tela. Mensagens em português: elas
// aparecem para o Henrique.
func (r RegrasVenda) Valida() error {
	f := r.Resolvida().Faixas
	for _, idade := range []int{f.IdadeParticular, f.IdadeFimFaixaTurma} {
		if idade < idadeMinimaFaixa || idade > idadeMaximaFaixa {
			return fmt.Errorf("as idades das faixas precisam ficar entre %d e %d anos", idadeMinimaFaixa, idadeMaximaFaixa)
		}
	}
	if f.IdadeFimFaixaTurma >= f.IdadeParticular {
		return fmt.Errorf("a idade do fim da faixa da turma (%d) precisa ser menor que a do curso particular (%d)", f.IdadeFimFaixaTurma, f.IdadeParticular)
	}
	conhecidas := map[ParteRegra]bool{}
	for _, p := range PartesDasRegras {
		conhecidas[p] = true
	}
	for parte, texto := range r.Textos {
		if !conhecidas[parte] {
			return fmt.Errorf("parte desconhecida: %q", parte)
		}
		if utf8.RuneCountInString(texto) > maxTextoRegra {
			return fmt.Errorf("o texto %q passou de %d caracteres", parte, maxTextoRegra)
		}
	}
	if len(r.Vocabulario) > maxTermosVocabulario {
		return fmt.Errorf("o vocabulário aceita no máximo %d termos", maxTermosVocabulario)
	}
	for _, t := range r.Vocabulario {
		if strings.TrimSpace(t.Evitar) == "" {
			return fmt.Errorf("todo termo do vocabulário precisa dizer a palavra a evitar")
		}
		if utf8.RuneCountInString(t.Evitar) > maxTamanhoTermo || utf8.RuneCountInString(t.Usar) > maxTamanhoTermo {
			return fmt.Errorf("cada termo do vocabulário aceita no máximo %d caracteres", maxTamanhoTermo)
		}
	}
	return nil
}

// faixaDe diz em que faixa a idade cai (ou "desconhecida" com idade <= 0).
func (f FaixasIdade) faixaDe(idade int) ParteRegra {
	switch {
	case idade <= 0:
		return ParteFaixaDesconhecida
	case idade >= f.IdadeParticular:
		return ParteFaixaParticular
	case idade >= f.IdadeFimFaixaTurma:
		return ParteFaixaFimTurma
	default:
		return ParteFaixaTurma
	}
}

// rotulo — como a faixa é dita no prompt ("15 a 16 anos").
func (f FaixasIdade) rotulo(parte ParteRegra) string {
	switch parte {
	case ParteFaixaParticular:
		return fmt.Sprintf("%d anos ou mais", f.IdadeParticular)
	case ParteFaixaFimTurma:
		if f.IdadeParticular-1 == f.IdadeFimFaixaTurma {
			return fmt.Sprintf("%d anos", f.IdadeFimFaixaTurma)
		}
		return fmt.Sprintf("%d a %d anos", f.IdadeFimFaixaTurma, f.IdadeParticular-1)
	case ParteFaixaTurma:
		return fmt.Sprintf("até %d anos", f.IdadeFimFaixaTurma-1)
	default:
		return "idade ainda desconhecida"
	}
}
