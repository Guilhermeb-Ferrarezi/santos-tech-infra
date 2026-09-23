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

func TestParserLeOEmailDoCliente(t *testing.T) {
	out, err := ParseModelReply(`{"bubbles":["Anotado!"],"answered":true,"answeredFromKb":false,
	  "schedulingRequest":{"kind":"experimental","studentName":"Caio",
	    "proposedDate":"2026-10-01","proposedTime":"09h30",
	    "clienteConfirmou":true,"clienteEmail":" Rodrigo@Gmail.com "}}`)
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	sr := out.SchedulingRequest
	if sr == nil {
		t.Fatal("schedulingRequest sumiu")
	}
	if sr.ClienteEmail != "Rodrigo@Gmail.com" {
		t.Errorf("clienteEmail = %q; o parser só apara as bordas", sr.ClienteEmail)
	}
	if GmailValido(sr.ClienteEmail) != "rodrigo@gmail.com" {
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
	if out2.SchedulingRequest.ClienteEmail != "" {
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
