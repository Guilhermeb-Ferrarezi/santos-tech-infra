package main

import (
	"fmt"
	"strings"
)

// Qualificação de lead — o que a escola sabe sobre a pessoa, e quanto ela vale.
//
// A REGRA QUE ORGANIZA TUDO: conversa primeiro, preço depois. Vale para
// qualquer atendimento — curso infantil, trilha de adolescente ou aula
// particular de adulto.
//
// Não é sonegar preço. Quem pergunta recebe; a diferença é QUANDO. O bot faz as
// perguntas-chave, e o número sai no fim. Recusar a quem insiste soa evasivo e
// queima o lead; responder de cara joga fora a única chance de descobrir quem é
// a pessoa, porque depois do preço a conversa costuma acabar.
//
// O QUE ISSO COMPRA: quem responde as perguntas, ouve o preço e AINDA ASSIM
// quer a aula experimental é um lead muito mais quente que quem só quis o
// número. Hoje os dois chegam iguais para a coordenação.

// Qualificacao — o dossiê de uma pessoa. Vive por CONTATO, não por conversa: a
// mesma família volta semanas depois, às vezes numa thread nova, e o que se
// descobriu sobre ela não pode morrer junto com a anterior.
type Qualificacao struct {
	ParaQuem        string // "proprio" | "filho" | "outro"
	AlunoNome       string
	AlunoIdade      int
	Interesse       string // programação, modelagem 3D, Excel, robótica...
	JaFazCurso      string // "sim" | "nao" | "" (ainda não perguntado)
	Disponibilidade string // "manhãs de quinta", "só sábado"
	Motivacao       string // nas palavras do cliente
	MotivacaoTipo   string // ver motivacoesValidas
	Observacoes     string

	// Sinais do que ACONTECEU. Escritos pelo código a partir de fatos, nunca
	// pelo modelo: um grau que o modelo atribui a si mesmo não classifica nada.
	PrecoInformado       bool
	AulaMarcada          bool
	PerguntasRespondidas int
}

// motivacoesValidas — por que a pessoa procura a escola.
//
// É o campo que mais vale para quem vai fechar a matrícula: "quer que o filho
// entre no mercado" e "o filho ama Roblox" pedem conversas completamente
// diferentes, e hoje essa distinção se perde entre o bot e a coordenação.
var motivacoesValidas = map[string]string{
	// Quando o curso é para a própria pessoa.
	"emprego":   "conseguir um emprego",
	"promocao":  "crescer no trabalho atual",
	"pessoal":   "desenvolvimento pessoal",
	"faculdade": "estudos / faculdade",
	// Quando é para um filho ou outra pessoa.
	"mercado_filho":    "preparar o filho para o mercado de trabalho",
	"habilidade_filho": "desenvolver uma habilidade que a criança já curte",
	"reforco":          "acompanhar a escola / reforço",
	"ocupar_tempo":     "ocupar o tempo com algo produtivo",
	"outro":            "outro motivo",
}

// paraQuemValidos — os três casos que mudam o curso indicado.
var paraQuemValidos = map[string]bool{"proprio": true, "filho": true, "outro": true}

// ── as perguntas ─────────────────────────────────────────────────────────────

// perguntaChave — uma coisa que a escola quer saber, e como perguntar.
type perguntaChave struct {
	campo    string
	pergunta string
	// soParaFilho: idade só faz sentido quando o curso é para uma criança.
	soParaFilho bool
}

// perguntasDaQualificacao — a ordem importa.
//
// Começa pelo que muda o rumo da conversa (para quem, idade) e termina no que
// só faz sentido depois de saber do que se está falando (motivação). Perguntar
// motivação antes de saber para quem é soa fora de lugar.
var perguntasDaQualificacao = []perguntaChave{
	{campo: "paraQuem", pergunta: "é pra você mesmo ou pra alguém da família?"},
	{campo: "alunoIdade", pergunta: "quantos anos ele(a) tem?", soParaFilho: true},
	{campo: "interesse", pergunta: "que área chama mais atenção — programação, jogos, modelagem 3D, informática?"},
	{campo: "jaFazCurso", pergunta: "já faz algum curso livre fora da escola?"},
	{campo: "motivacao", pergunta: "o que fez você procurar o curso agora?"},
	{campo: "disponibilidade", pergunta: "quais dias da semana costumam ser melhores?"},
}

// Falta diz o que ainda não se sabe, na ordem de perguntar.
//
// É calculado em Go e entregue pronto ao modelo a cada mensagem. Pedir ao
// modelo que "lembre o que já perguntou" é justamente o que ele faz mal: ele
// repete pergunta já respondida, ou pula direto para o preço.
func (q Qualificacao) Falta() []perguntaChave {
	respondido := map[string]bool{
		"paraQuem":        q.ParaQuem != "",
		"alunoIdade":      q.AlunoIdade > 0,
		"interesse":       q.Interesse != "",
		"jaFazCurso":      q.JaFazCurso != "",
		"motivacao":       q.Motivacao != "" || q.MotivacaoTipo != "",
		"disponibilidade": q.Disponibilidade != "",
	}
	var falta []perguntaChave
	for _, p := range perguntasDaQualificacao {
		if p.soParaFilho && q.ParaQuem != "filho" && q.ParaQuem != "outro" {
			continue
		}
		if !respondido[p.campo] {
			falta = append(falta, p)
		}
	}
	return falta
}

// Respondidas conta quantas perguntas-chave já têm resposta.
func (q Qualificacao) Respondidas() int {
	total := len(perguntasDaQualificacao)
	if q.ParaQuem != "filho" && q.ParaQuem != "outro" {
		total-- // idade não se aplica
	}
	return total - len(q.Falta())
}

// PodeFalarPreco diz se a etapa de qualificação já rendeu o suficiente.
//
// O corte é TRÊS perguntas, não todas. Exigir as seis transformaria a trava num
// interrogatório e daria ao cliente teimoso a impressão de que o bot está
// fugindo — que é exatamente o oposto do que se quer. Três respostas já
// separam quem conversa de quem só quer o número.
func (q Qualificacao) PodeFalarPreco() bool {
	return q.Respondidas() >= 3
}

// ── o grau ───────────────────────────────────────────────────────────────────

type GrauQualificacao string

const (
	GrauMuitoQualificado GrauQualificacao = "muito_qualificado"
	GrauQualificado      GrauQualificacao = "qualificado"
	GrauMorno            GrauQualificacao = "morno"
	GrauFrio             GrauQualificacao = "frio"
)

// Grau sai do que a pessoa FEZ, não de uma nota que o modelo atribui.
//
//	muito qualificado — respondeu, ouviu o preço e mesmo assim marcou a aula
//	qualificado       — respondeu e marcou (sem chegar no preço)
//	morno             — respondeu parte, não marcou
//	frio              — não respondeu nada, só quis o preço
func (q Qualificacao) Grau() GrauQualificacao {
	respondidas := q.Respondidas()
	switch {
	case q.AulaMarcada && q.PrecoInformado && respondidas >= 3:
		return GrauMuitoQualificado
	case q.AulaMarcada:
		return GrauQualificado
	case respondidas >= 2:
		return GrauMorno
	default:
		return GrauFrio
	}
}

// GrauLegivel — como o grau aparece para gente, no painel e no aviso.
func (g GrauQualificacao) Legivel() string {
	switch g {
	case GrauMuitoQualificado:
		return "muito qualificado (respondeu tudo, soube o preço e marcou mesmo assim)"
	case GrauQualificado:
		return "qualificado (conversou e marcou a experimental)"
	case GrauMorno:
		return "morno (respondeu em parte, não marcou)"
	default:
		return "frio (não respondeu as perguntas)"
	}
}

// ── juntar o que chega com o que já se sabe ──────────────────────────────────

// Merge incorpora o que o modelo descobriu NESTE turno.
//
// Campo vazio NUNCA apaga o que já se sabia. O modelo manda só o que ouviu
// agora; se ele omitir a idade numa mensagem sobre horário, isso não significa
// que a escola esqueceu a idade da criança. Esquecer é o defeito mais caro
// numa memória: obriga a perguntar de novo, e perguntar de novo o que a pessoa
// já respondeu é o jeito mais rápido de parecer um robô.
func (q Qualificacao) Merge(novo Qualificacao) Qualificacao {
	juntado := q
	if v := strings.TrimSpace(novo.ParaQuem); v != "" && paraQuemValidos[v] {
		juntado.ParaQuem = v
	}
	if v := strings.TrimSpace(novo.AlunoNome); v != "" {
		juntado.AlunoNome = v
	}
	if novo.AlunoIdade > 0 && novo.AlunoIdade < 120 {
		juntado.AlunoIdade = novo.AlunoIdade
	}
	if v := strings.TrimSpace(novo.Interesse); v != "" {
		juntado.Interesse = v
	}
	if v := strings.ToLower(strings.TrimSpace(novo.JaFazCurso)); v == "sim" || v == "nao" || v == "não" {
		juntado.JaFazCurso = strings.ReplaceAll(v, "ã", "a")
	}
	if v := strings.TrimSpace(novo.Disponibilidade); v != "" {
		juntado.Disponibilidade = v
	}
	if v := strings.TrimSpace(novo.Motivacao); v != "" {
		juntado.Motivacao = v
	}
	if v := strings.TrimSpace(novo.MotivacaoTipo); v != "" {
		if _, ok := motivacoesValidas[v]; ok {
			juntado.MotivacaoTipo = v
		}
	}
	if v := strings.TrimSpace(novo.Observacoes); v != "" {
		// Observação nova não substitui a anterior: acumula, porque cada uma é
		// um fato diferente sobre a mesma pessoa.
		if juntado.Observacoes == "" {
			juntado.Observacoes = v
		} else if !strings.Contains(juntado.Observacoes, v) {
			juntado.Observacoes = juntado.Observacoes + " · " + v
		}
	}
	// Os sinais só andam para frente: preço informado não desinforma, aula
	// marcada não desmarca por omissão do modelo.
	juntado.PrecoInformado = q.PrecoInformado || novo.PrecoInformado
	juntado.AulaMarcada = q.AulaMarcada || novo.AulaMarcada
	juntado.PerguntasRespondidas = juntado.Respondidas()
	return juntado
}

// Vazia diz se ainda não se sabe nada da pessoa.
func (q Qualificacao) Vazia() bool {
	return q.ParaQuem == "" && q.AlunoNome == "" && q.AlunoIdade == 0 &&
		q.Interesse == "" && q.JaFazCurso == "" && q.Disponibilidade == "" &&
		q.Motivacao == "" && q.MotivacaoTipo == "" && q.Observacoes == ""
}

// ── como isso chega ao prompt ────────────────────────────────────────────────

// BlocoDoDossie escreve o que já se sabe da pessoa.
//
// Vai em toda mensagem. É o que impede o bot de perguntar a idade da criança
// pela terceira vez — e o que dá a ele o "oi de novo, como foi a aula do Caio?"
// quando a família volta semanas depois.
func (q Qualificacao) BlocoDoDossie() string {
	if q.Vazia() {
		return "# O que já sei desta pessoa\nNada ainda — é a primeira conversa, ou ela ainda não contou nada.\n\n"
	}
	var b strings.Builder
	b.WriteString("# O que já sei desta pessoa (NÃO pergunte de novo)\n")
	linha := func(rotulo, valor string) {
		if strings.TrimSpace(valor) != "" {
			fmt.Fprintf(&b, "- %s: %s\n", rotulo, valor)
		}
	}
	switch q.ParaQuem {
	case "proprio":
		linha("O curso é", "para a própria pessoa com quem você está falando")
	case "filho":
		linha("O curso é", "para um filho(a)")
	case "outro":
		linha("O curso é", "para outra pessoa da família")
	}
	linha("Nome do aluno", q.AlunoNome)
	if q.AlunoIdade > 0 {
		fmt.Fprintf(&b, "- Idade: %d anos\n", q.AlunoIdade)
	}
	linha("Interesse", q.Interesse)
	switch q.JaFazCurso {
	case "sim":
		linha("Já faz curso livre", "sim")
	case "nao":
		linha("Já faz curso livre", "não")
	}
	linha("Disponibilidade", q.Disponibilidade)
	linha("Motivação", q.Motivacao)
	if d, ok := motivacoesValidas[q.MotivacaoTipo]; ok && q.Motivacao == "" {
		linha("Motivação", d)
	}
	linha("Outras anotações", q.Observacoes)
	if q.AulaMarcada {
		b.WriteString("- Já tem aula experimental marcada.\n")
	}
	if q.PrecoInformado {
		b.WriteString("- Já sabe os valores (você já informou).\n")
	}
	b.WriteString("\n")
	return b.String()
}

// BlocoDasRegras escreve a etapa de qualificação e a trava do preço.
//
// As perguntas que faltam vêm calculadas, não deduzidas pelo modelo: é a
// diferença entre "lembre-se de qualificar" (que ele ignora quando a conversa
// aquece) e "a próxima coisa que falta saber é X".
func (q Qualificacao) BlocoDasRegras() string {
	var b strings.Builder
	b.WriteString("# Qualificação (antes de falar de preço)\n")
	b.WriteString("Antes de valores, descubra quem é a pessoa. São perguntas CURTAS, uma por mensagem, no meio da conversa — nunca um formulário, nunca duas de uma vez. Só pergunte o que ainda não está no bloco acima.\n")

	falta := q.Falta()
	if len(falta) == 0 {
		b.WriteString("Você já sabe o suficiente sobre esta pessoa. NÃO faça mais perguntas de qualificação — conduza para a aula experimental.\n")
	} else {
		b.WriteString("Ainda falta saber, nesta ordem:\n")
		for i, p := range falta {
			fmt.Fprintf(&b, "%d. %s\n", i+1, p.pergunta)
		}
		b.WriteString("Encaixe a PRÓXIMA da lista quando fizer sentido na conversa. Não despeje todas.\n")
	}

	// A trava.
	b.WriteString("\n## Preço\n")
	if q.PodeFalarPreco() || q.PrecoInformado {
		b.WriteString("Esta pessoa já conversou o suficiente: pode informar valores normalmente quando ela perguntar, sempre dizendo o que está incluído e reconduzindo para a aula experimental.\n")
	} else {
		b.WriteString("AINDA NÃO informe valores — nem mensalidade, nem matrícula, nem material, nem faixa de preço, e isso vale para curso infantil, trilha de adolescente E aula particular de adulto.\n")
		b.WriteString("Se o cliente perguntar o preço agora: NÃO diga que não pode falar e NÃO invente desculpa. Responda que já passa os valores, e no MESMO balão faça a próxima pergunta da lista acima. Exemplo: \"Já te falo os valores! Só pra eu te indicar certo: é pra você ou pra alguém da família?\"\n")
		b.WriteString("Se ele insistir uma SEGUNDA vez sem responder nada, informe o valor — perder o lead por causa da trava é pior que qualificar mal.\n")
	}
	b.WriteString("\n")
	return b.String()
}
