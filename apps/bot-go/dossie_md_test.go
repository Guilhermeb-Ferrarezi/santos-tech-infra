package main

import (
	"strings"
	"testing"
	"time"
)

// O documento é para GENTE ler — no Drive, no celular, colado numa mensagem
// para o professor. O banco é para o bot.
func TestDossieEmMarkdown(t *testing.T) {
	agora := time.Date(2026, 9, 23, 18, 0, 0, 0, brLocation)
	d := DossieCliente{
		Telefone: "5516991590787",
		Nome:     "Rodrigo",
		Qualificacao: Qualificacao{}.
			Merge(Qualificacao{ParaQuem: "filho", AlunoNome: "Caio", AlunoIdade: 14}).
			Merge(Qualificacao{Interesse: "programação", JaFazCurso: "nao"}).
			Merge(Qualificacao{Motivacao: "quer que ele entre no mercado de trabalho",
				MotivacaoTipo: "mercado_filho", AulaMarcada: true}),
		AulaEm: time.Date(2026, 10, 1, 9, 30, 0, 0, brLocation),
		Conversa: []LinhaDaConversa{
			{Quando: agora.Add(-2 * time.Hour), DeQuem: "cliente", Texto: "oi, quero saber dos cursos"},
			{Quando: agora.Add(-1 * time.Hour), DeQuem: "bot", Texto: "Boa tarde! É pra você ou pra alguém da família?"},
		},
		TotalMensagens: 2,
	}
	md := d.Markdown(agora)

	// O cabeçalho precisa dizer quem é e quanto vale, sem rolar a página.
	if !strings.HasPrefix(md, "# Rodrigo — (16) 99159-0787") {
		t.Errorf("cabeçalho errado: %q", strings.SplitN(md, "\n", 2)[0])
	}
	for _, esperado := range []string{
		"Caio", "14 anos", "programação",
		"quer que ele entre no mercado de trabalho",
		"01/10/2026 às 09:30",
		"ainda vai acontecer",
		"[x] Marcou a aula experimental",
		"oi, quero saber dos cursos",
	} {
		if !strings.Contains(md, esperado) {
			t.Errorf("faltou no documento: %q", esperado)
		}
	}
	// O que ainda não se sabe tem que aparecer — é o que diz o que perguntar.
	if !strings.Contains(md, "Ainda não sabemos") {
		t.Error("o documento não diz o que falta descobrir")
	}
	// E precisa avisar que editar o arquivo não muda o que o bot sabe.
	if !strings.Contains(md, "NÃO muda o que o bot sabe") {
		t.Error("sem o aviso, alguém vai editar o arquivo esperando efeito")
	}
}

// Pessoa que só mandou "oi" também tem documento — e ele precisa dizer
// honestamente que não se sabe nada, em vez de parecer vazio por erro.
func TestDossieDeQuemNaoContouNada(t *testing.T) {
	md := DossieCliente{Telefone: "5516999998888"}.Markdown(time.Now())
	if !strings.Contains(md, "Ainda não contou nada sobre si") {
		t.Error("dossiê vazio deveria se anunciar como vazio")
	}
	if !strings.Contains(md, "frio") {
		t.Error("quem não contou nada é lead frio, e isso tem que estar no topo")
	}
}

func TestNomeDoArquivoComecaPeloTelefone(t *testing.T) {
	casos := []struct{ nome, telefone, esperado string }{
		{"Rodrigo", "5516991590787", "5516991590787 - Rodrigo.md"},
		{"", "5516991590787", "5516991590787.md"},
		// Emoji e pontuação no nome do WhatsApp são a regra, não a exceção.
		{"Má/ria 🎉 Souza", "5516991590787", "5516991590787 - Mria Souza.md"},
	}
	for _, c := range casos {
		got := DossieCliente{Nome: c.nome, Telefone: c.telefone}.NomeDoArquivo()
		if got != c.esperado {
			t.Errorf("NomeDoArquivo(%q) = %q, esperado %q", c.nome, got, c.esperado)
		}
	}
}

func TestFormataTelefone(t *testing.T) {
	casos := map[string]string{
		"5516991590787": "(16) 99159-0787",
		"551633334444":  "(16) 3333-4444",
		"":              "",
		"12345":         "12345",
	}
	for entrada, esperado := range casos {
		if got := FormataTelefone(entrada); got != esperado {
			t.Errorf("FormataTelefone(%q) = %q, esperado %q", entrada, got, esperado)
		}
	}
}
