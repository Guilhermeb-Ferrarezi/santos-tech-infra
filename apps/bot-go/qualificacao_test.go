package main

import (
	"strings"
	"testing"
	"time"
)

// A trava de preço só vale se ela não travar demais nem de menos.

func TestPrecoLiberaDepoisDeTresRespostas(t *testing.T) {
	var q Qualificacao
	if q.PodeFalarPreco() {
		t.Error("quem não respondeu nada não pode receber o preço de cara")
	}

	q = q.Merge(Qualificacao{ParaQuem: "filho"})
	if q.PodeFalarPreco() {
		t.Error("uma resposta ainda não é conversa")
	}

	q = q.Merge(Qualificacao{AlunoIdade: 14})
	if q.PodeFalarPreco() {
		t.Error("duas respostas ainda não")
	}

	q = q.Merge(Qualificacao{Interesse: "programação"})
	if !q.PodeFalarPreco() {
		t.Error("com três respostas o preço tem que sair — insistir vira interrogatório")
	}
}

// Adulto não tem a pergunta da idade, então três respostas dele são três de
// cinco, não de seis. Se a conta não descontasse, o adulto precisaria responder
// uma pergunta que ninguém vai fazer.
func TestAdultoNaoPrecisaResponderIdade(t *testing.T) {
	adulto := Qualificacao{}.Merge(Qualificacao{
		ParaQuem: "proprio", Interesse: "Excel", JaFazCurso: "nao",
	})
	if !adulto.PodeFalarPreco() {
		t.Error("adulto com três respostas deveria poder ouvir o preço")
	}
	for _, p := range adulto.Falta() {
		if p.campo == "alunoIdade" {
			t.Error("a pergunta da idade não pode aparecer para adulto")
		}
	}
}

func TestFaltaNaoRepetePerguntaRespondida(t *testing.T) {
	q := Qualificacao{}.Merge(Qualificacao{ParaQuem: "filho", AlunoIdade: 9})
	for _, p := range q.Falta() {
		if p.campo == "paraQuem" || p.campo == "alunoIdade" {
			t.Errorf("pergunta %q já foi respondida e voltou na lista", p.campo)
		}
	}
	// E o que falta continua lá.
	campos := map[string]bool{}
	for _, p := range q.Falta() {
		campos[p.campo] = true
	}
	for _, esperado := range []string{"interesse", "jaFazCurso", "motivacao", "disponibilidade"} {
		if !campos[esperado] {
			t.Errorf("%q sumiu da lista do que falta", esperado)
		}
	}
}

// O grau sai do que a pessoa FEZ. É a régua que separa quem vale uma ligação
// de quem só passou por aqui.
func TestGrauSaiDoQueAconteceu(t *testing.T) {
	respondeuTudo := Qualificacao{}.Merge(Qualificacao{
		ParaQuem: "filho", AlunoIdade: 14, Interesse: "programação",
		JaFazCurso: "nao", Motivacao: "quer que ele aprenda cedo",
		Disponibilidade: "manhãs",
	})

	casos := []struct {
		nome string
		q    Qualificacao
		grau GrauQualificacao
	}{
		{"só quis o preço", Qualificacao{PrecoInformado: true}, GrauFrio},
		{"nem respondeu nem marcou", Qualificacao{}, GrauFrio},
		{"respondeu em parte, não marcou",
			Qualificacao{}.Merge(Qualificacao{ParaQuem: "filho", AlunoIdade: 9}), GrauMorno},
		{"conversou e marcou, sem falar de preço",
			respondeuTudo.Merge(Qualificacao{AulaMarcada: true}), GrauQualificado},
		{"soube o preço e marcou mesmo assim",
			respondeuTudo.Merge(Qualificacao{AulaMarcada: true, PrecoInformado: true}), GrauMuitoQualificado},
	}
	for _, c := range casos {
		if got := c.q.Grau(); got != c.grau {
			t.Errorf("%s: grau = %q, esperado %q", c.nome, got, c.grau)
		}
	}
}

// Esquecer é o defeito mais caro numa memória: obriga a perguntar de novo, e
// perguntar de novo o que a pessoa já respondeu é o jeito mais rápido de
// parecer um robô.
func TestMergeNaoApagaOQueJaSeSabia(t *testing.T) {
	antes := Qualificacao{}.Merge(Qualificacao{
		ParaQuem: "filho", AlunoNome: "Caio", AlunoIdade: 14,
		Interesse: "programação", JaFazCurso: "nao",
	})

	// Turno seguinte fala só de horário.
	depois := antes.Merge(Qualificacao{Disponibilidade: "manhãs de quinta"})

	if depois.AlunoNome != "Caio" || depois.AlunoIdade != 14 {
		t.Error("o dossiê esqueceu o aluno numa mensagem que não falava dele")
	}
	if depois.Interesse != "programação" || depois.JaFazCurso != "nao" {
		t.Error("o dossiê esqueceu o que já sabia")
	}
	if depois.Disponibilidade != "manhãs de quinta" {
		t.Error("o dado novo não entrou")
	}

	// Sinais só andam para frente.
	comPreco := depois.Merge(Qualificacao{PrecoInformado: true})
	if !comPreco.Merge(Qualificacao{}).PrecoInformado {
		t.Error("preço informado não pode desinformar por omissão do modelo")
	}
}

func TestMergeRecusaValorInventado(t *testing.T) {
	q := Qualificacao{}.Merge(Qualificacao{
		ParaQuem:      "sei lá",  // fora do domínio
		MotivacaoTipo: "vontade", // não está na lista
		AlunoIdade:    -3,
		JaFazCurso:    "talvez",
	})
	if q.ParaQuem != "" {
		t.Errorf("paraQuem aceitou %q", q.ParaQuem)
	}
	if q.MotivacaoTipo != "" {
		t.Errorf("motivacaoTipo aceitou %q", q.MotivacaoTipo)
	}
	if q.AlunoIdade != 0 {
		t.Errorf("idade aceitou %d", q.AlunoIdade)
	}
	if q.JaFazCurso != "" {
		t.Errorf("jaFazCurso aceitou %q", q.JaFazCurso)
	}
	// "não" com acento é a forma que o modelo escreve em português.
	if got := (Qualificacao{}).Merge(Qualificacao{JaFazCurso: "não"}).JaFazCurso; got != "nao" {
		t.Errorf("jaFazCurso com acento virou %q", got)
	}
}

// O prompt precisa dizer ao modelo o que falta — pedir que ele "lembre de
// qualificar" é justamente o que ele faz mal quando a conversa aquece.
func TestPromptTravaOPrecoAteConversar(t *testing.T) {
	cfg := TenantConfig{EscolaAbre: "08:00", EscolaFecha: "22:00", AulaDuracaoMin: 60}
	agora := time.Unix(1789000000, 0).UTC()

	novo := BuildPrompt(cfg, ConversationContext{}, "quanto custa?", agora)
	if !strings.Contains(novo, "AINDA NÃO informe valores") {
		t.Error("sem qualificação nenhuma, o prompt deveria travar o preço")
	}
	if !strings.Contains(novo, "aula particular de adulto") {
		t.Error("a trava precisa dizer que vale para adulto também")
	}
	if !strings.Contains(novo, "Nada ainda") {
		t.Error("o dossiê vazio deveria se anunciar como vazio")
	}
	// Travar não é fugir: o prompt tem que ensinar a responder.
	if !strings.Contains(novo, "Já te falo os valores") {
		t.Error("o prompt não dá a saída para quem pergunta o preço cedo")
	}
	if !strings.Contains(novo, "insistir uma SEGUNDA vez") {
		t.Error("sem a válvula de escape, a trava vira evasiva e queima o lead")
	}

	conhecido := ConversationContext{Qualificacao: Qualificacao{}.Merge(Qualificacao{
		ParaQuem: "filho", AlunoNome: "Caio", AlunoIdade: 14, Interesse: "programação",
	})}
	p2 := BuildPrompt(cfg, conhecido, "quanto custa?", agora)
	if strings.Contains(p2, "AINDA NÃO informe valores") {
		t.Error("com três respostas o preço tem que estar liberado")
	}
	if !strings.Contains(p2, "Caio") || !strings.Contains(p2, "14 anos") {
		t.Error("o dossiê não chegou ao prompt")
	}
	if !strings.Contains(p2, "NÃO pergunte de novo") {
		t.Error("o prompt não avisa para não repetir o que já sabe")
	}
	// E não pode pedir de novo o que já tem.
	if strings.Contains(p2, "quantos anos ele(a) tem?") {
		t.Error("o prompt está mandando perguntar a idade que já sabe")
	}
}

func TestParserLeAQualificacao(t *testing.T) {
	out, err := ParseModelReply(`{"bubbles":["Legal!"],"answered":true,"answeredFromKb":false,
	  "qualificacao":{"paraQuem":"filho","alunoNome":"Caio","alunoIdade":14,
	    "interesse":"programação","jaFazCurso":"nao","motivacaoTipo":"mercado_filho",
	    "motivacao":"quer que ele entre no mercado","observacoes":"mora perto"}}`)
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	q := out.Qualificacao
	if q == nil {
		t.Fatal("qualificacao sumiu")
	}
	if q.ParaQuem != "filho" || q.AlunoIdade != 14 || q.MotivacaoTipo != "mercado_filho" {
		t.Errorf("campos vieram errados: %+v", q)
	}

	// Objeto vazio não vira escrita no banco.
	out2, _ := ParseModelReply(`{"bubbles":["oi"],"answered":true,"answeredFromKb":false,
	  "qualificacao":{}}`)
	if out2.Qualificacao != nil {
		t.Error("qualificação vazia não deveria virar dossiê")
	}
}
