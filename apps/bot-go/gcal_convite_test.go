package main

import (
	"strings"
	"testing"
	"time"
)

// O cliente entra na própria agenda com os mesmos lembretes da escola — mas só
// com conta Google. Endereço de outro provedor recebe o convite por e-mail e
// não ganha lembrete nenhum, e prometer lembrete que não existe é pior que não
// pedir o e-mail.
func TestGmailValido(t *testing.T) {
	casos := []struct{ entrada, saida string }{
		{"rodrigo@gmail.com", "rodrigo@gmail.com"},
		{"  Rodrigo.Santos@Gmail.com ", "rodrigo.santos@gmail.com"},
		{"alguem@googlemail.com", "alguem@googlemail.com"},

		{"", ""},
		{"rodrigo@hotmail.com", ""},
		{"rodrigo@outlook.com", ""},
		{"rodrigo@empresa.com.br", ""},
		{"gmail.com", ""},
		{"@gmail.com", ""},
		{"rodrigo@", ""},
		// Transcrição de áudio costuma trazer espaço e pontuação no meio; é
		// endereço errado, não endereço estranho.
		{"rodrigo santos@gmail.com", ""},
		{"rodrigo,santos@gmail.com", ""},
	}
	for _, c := range casos {
		if got := GmailValido(c.entrada); got != c.saida {
			t.Errorf("GmailValido(%q) = %q, esperado %q", c.entrada, got, c.saida)
		}
	}
}

// Os três lembretes na agenda: um dia, quatro horas e uma hora antes.
func TestLembretesDaAgendaSaoTres(t *testing.T) {
	esperado := []int{24 * 60, 4 * 60, 60}
	if len(lembretesDaAula) != len(esperado) {
		t.Fatalf("são %d lembretes, esperado %d", len(lembretesDaAula), len(esperado))
	}
	for i, m := range esperado {
		if lembretesDaAula[i] != m {
			t.Errorf("lembrete %d = %d min, esperado %d", i, lembretesDaAula[i], m)
		}
	}
}

// O WhatsApp tem os mesmos três momentos, e com textos DIFERENTES: mandar a
// mesma frase três vezes ensina o cliente a ignorar a quarta.
func TestLembretesDoWhatsAppSaoTresEDiferentes(t *testing.T) {
	momentos := []TipoLembrete{LembreteVespera, LembreteQuatroHoras, LembreteUmaHora}
	visto := map[string]TipoLembrete{}
	for _, m := range momentos {
		variantes := textosLembrete[m]
		if len(variantes) == 0 {
			t.Errorf("%s não tem texto", m)
			continue
		}
		for _, v := range variantes {
			if antes, repetido := visto[v]; repetido {
				t.Errorf("%s repete o texto de %s: %q", m, antes, v)
			}
			visto[v] = m
		}
		if _, ok := antecedenciaLembrete[m]; !ok {
			t.Errorf("%s não tem antecedência definida", m)
		}
	}
	if antecedenciaLembrete[LembreteUmaHora] != time.Hour {
		t.Error("o lembrete de uma hora não está a uma hora da aula")
	}
}

func TestPromptPedeGmailDepoisDeConfirmar(t *testing.T) {
	cfg := TenantConfig{
		EscolaAbre: "08:00", EscolaFecha: "22:00", AulaDuracaoMin: 60,
		AgendaAutoConfirm: true,
	}
	p := BuildPrompt(cfg, ConversationContext{}, "pode marcar", time.Unix(1789000000, 0).UTC())

	if !strings.Contains(p, `"clienteEmail"`) {
		t.Error("o schema do prompt não tem clienteEmail")
	}
	if !strings.Contains(p, "NÃO reemita") {
		t.Error("o prompt não proíbe reemitir o agendamento junto com o e-mail — era assim que a aula era remarcada sem querer")
	}
	if !strings.Contains(p, "LOGO DEPOIS de confirmar") {
		t.Error("o prompt não diz QUANDO pedir o e-mail — pedir antes do aceite vira obstáculo")
	}
	if !strings.Contains(p, "GMAIL") {
		t.Error("o prompt não deixa claro que precisa ser Gmail")
	}

	// Com o agendamento automático desligado o bot não marca nada, então pedir
	// e-mail para uma agenda que ninguém vai preencher só atrapalha.
	cfg.AgendaAutoConfirm = false
	if p2 := BuildPrompt(cfg, ConversationContext{}, "pode marcar", time.Unix(1789000000, 0).UTC()); strings.Contains(p2, "LOGO DEPOIS de confirmar") {
		t.Error("sem auto-confirm o bot não deveria pedir Gmail")
	}
}

func TestParserLeOEmailNoNivelDeCima(t *testing.T) {
	// O bot pede o Gmail DEPOIS de marcar, então ele chega numa mensagem que
	// não fala de horário. O campo é de topo justamente para o modelo não
	// precisar reemitir o agendamento — reemitir remarcava a aula sem querer.
	out, err := ParseModelReply(`{"bubbles":["Anotado!"],"answered":true,"answeredFromKb":false,
	  "clienteEmail":" Rodrigo@Gmail.com "}`)
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if out.ClienteEmail != "Rodrigo@Gmail.com" {
		t.Errorf("clienteEmail = %q; o parser só apara as bordas", out.ClienteEmail)
	}
	if out.SchedulingRequest != nil {
		t.Error("mandar o e-mail NÃO pode virar pedido de agendamento")
	}
	if GmailValido(out.ClienteEmail) != "rodrigo@gmail.com" {
		t.Error("a normalização para o convite acontece no GmailValido")
	}

	// Sem o campo, a aula é marcada do mesmo jeito — o e-mail é opcional.
	out2, err := ParseModelReply(`{"bubbles":["ok"],"answered":true,"answeredFromKb":false,
	  "schedulingRequest":{"kind":"experimental","proposedDate":"2026-10-01",
	    "proposedTime":"09h30","clienteConfirmou":true}}`)
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if out2.SchedulingRequest == nil || !out2.SchedulingRequest.ClienteConfirmou {
		t.Error("sem e-mail o agendamento continua valendo")
	}
	if out2.ClienteEmail != "" {
		t.Error("campo ausente tem que chegar vazio")
	}
}

// O título da aula vai para a agenda da escola e para o Notion. Uma frase no
// lugar do nome ("Não informado (filho do responsável)") polui a grade que
// Henrique e os professores leem todo dia.
func TestNomeDoAlunoRecusaFrasesNoLugarDeNome(t *testing.T) {
	casos := []struct{ informado, responsavel, esperado string }{
		{"Caio", "Rodrigo", "Caio"},
		{"  Júlio T  ", "Rodrigo", "Júlio T"},

		// O que o modelo escreveu de verdade em produção.
		{"Não informado (filho do responsável)", "Rodrigo", "Rodrigo"},
		{"não informado", "Rodrigo", "Rodrigo"},
		{"a confirmar", "Rodrigo", "Rodrigo"},
		{"filho do Rodrigo", "Rodrigo", "Rodrigo"},
		{"", "Rodrigo", "Rodrigo"},

		// Sem responsável também não vale gravar a frase.
		{"Não informado", "", "a confirmar"},
		{"", "", "a confirmar"},
	}
	for _, c := range casos {
		if got := NomeDoAluno(c.informado, c.responsavel); got != c.esperado {
			t.Errorf("NomeDoAluno(%q, %q) = %q, esperado %q",
				c.informado, c.responsavel, got, c.esperado)
		}
	}

	// E o título montado a partir dele continua legível.
	quando := time.Date(2026, 10, 2, 10, 0, 0, 0, brLocation)
	titulo := TituloAulaBot(NomeDoAluno("Não informado (filho do responsável)", "Rodrigo"), quando)
	if !strings.Contains(titulo, "Rodrigo") || strings.Contains(titulo, "informado") {
		t.Errorf("título ficou %q", titulo)
	}
}

// Endereço malformado não pode chegar ao Google: ele responde 400 e derruba a
// criação do evento INTEIRO — a escola ficaria sem a aula na agenda por causa
// de um e-mail que o cliente digitou errado.
func TestGmailValidoBarraEnderecoQueDerrubariaOEvento(t *testing.T) {
	for _, ruim := range []string{
		"rodrigo@@gmail.com",
		"rodrigo@gmail.com@gmail.com",
		".rodrigo@gmail.com",
		"rodrigo.@gmail.com",
		"rodrigo..santos@gmail.com",
		"@gmail.com",
		"rodrigo!santos@gmail.com",
		"rodrigo<santos@gmail.com",
	} {
		if got := GmailValido(ruim); got != "" {
			t.Errorf("GmailValido(%q) = %q; deveria recusar", ruim, got)
		}
	}
	// E os válidos continuam passando — recusar demais deixaria o cliente sem
	// a agenda sem motivo.
	for _, bom := range []string{
		"rodrigo.santos@gmail.com",
		"rodrigo_santos@gmail.com",
		"rodrigo-santos@gmail.com",
		"rodrigo+escola@gmail.com",
		"rodrigo123@gmail.com",
	} {
		if GmailValido(bom) == "" {
			t.Errorf("GmailValido(%q) recusou um endereço válido", bom)
		}
	}
}

// Dois filhos na mesma conversa não podem virar a mesma pessoa: a remarcação
// compara o nome do aluno, então colapsar os dois no nome do responsável faria
// a aula do segundo ARQUIVAR a do primeiro.
func TestNomeDoAlunoNaoColapsaDoisFilhos(t *testing.T) {
	primeiro := NomeDoAluno("Caio", "Rodrigo")
	segundo := NomeDoAluno("Manuela", "Rodrigo")
	if MesmoAluno(primeiro, segundo) {
		t.Error("dois filhos viraram a mesma pessoa")
	}

	// "Filho" também é sobrenome. Barrar a palavra em qualquer posição apagava
	// o nome de gente de verdade.
	if got := NomeDoAluno("Antônio Barbosa Filho", "Rodrigo"); got != "Antônio Barbosa Filho" {
		t.Errorf("sobrenome Filho virou %q", got)
	}
	if got := NomeDoAluno("Maria Filha de Souza", "Rodrigo"); got != "Maria Filha de Souza" {
		t.Errorf("nome com 'Filha' no meio virou %q", got)
	}

	// Mas a descrição no lugar do nome continua sendo trocada.
	if got := NomeDoAluno("filho do responsável", "Rodrigo"); got != "Rodrigo" {
		t.Errorf("descrição no lugar do nome virou %q", got)
	}
}
