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

// O escopo pedido precisa ser drive.file, nunca drive.
//
// drive dá ao bot o Drive INTEIRO de quem autorizou — planilhas da escola,
// fotos de família, tudo. drive.file dá só o que ele mesmo criou. A diferença
// é entre um bot que escreve os dossiês e um bot que pode ler a vida da pessoa.
// Cada conta autoriza UM propósito. Pedir Drive na agenda pessoal do Henrique
// seria pedir acesso que ele não precisa dar; e a conta da diretoria, onde
// ficam os arquivos sensíveis da empresa, não deve virar agenda.
func TestCadaContaAutorizaSoOSeuProposito(t *testing.T) {
	if driveScope != "https://www.googleapis.com/auth/drive.file" {
		t.Errorf("escopo = %q; tem que ser drive.file, não drive", driveScope)
	}
	g := NewGCalClient("id", "secret", "https://exemplo/callback", nil)

	agenda := g.URLDeAutorizacao("estado", UsoAgenda)
	if !strings.Contains(agenda, "calendar.events") {
		t.Error("o link da agenda não pede o calendário")
	}
	if strings.Contains(agenda, "drive") {
		t.Error("o link da AGENDA está pedindo Drive — acesso que ninguém precisa dar")
	}

	drive := g.URLDeAutorizacao("estado", UsoDrive)
	if !strings.Contains(drive, "drive.file") {
		t.Error("o link do Drive não pede o Drive")
	}
	if strings.Contains(drive, "calendar") {
		t.Error("o link do DRIVE está pedindo agenda")
	}
	// Se o escopo largo entrar por descuido, o bot passaria a enxergar os 5 TB
	// de arquivos sensíveis da diretoria.
	if strings.Contains(drive, "auth%2Fdrive+") || strings.Contains(drive, "auth%2Fdrive&") {
		t.Error("o link está pedindo o Drive INTEIRO")
	}

	// Sem estes dois o Google não devolve refresh token, e a integração morre
	// sozinha em uma hora.
	for _, link := range []string{agenda, drive} {
		for _, obrigatorio := range []string{"access_type=offline", "prompt=consent"} {
			if !strings.Contains(link, obrigatorio) {
				t.Errorf("faltou %s no link", obrigatorio)
			}
		}
	}

	// Parâmetro inventado não pode virar autorização larga por acidente.
	if UsoValido("tudo") || UsoValido("") {
		t.Error("uso inválido foi aceito")
	}
}

// O que foi PEDIDO e o que foi CONCEDIDO podem divergir — e divergiram.
//
// A conta da diretoria devolveu um token com Drive COMPLETO mesmo tendo sido
// pedido só drive.file, porque já havia concedido esse acesso a este mesmo app
// antes e o Google somou as permissões antigas. O link não pode mais somar, e
// o bot não pode mais usar um token largo demais.
func TestLinkNaoSomaPermissoesAntigas(t *testing.T) {
	g := NewGCalClient("id", "secret", "https://exemplo/callback", nil)
	for _, uso := range []string{UsoAgenda, UsoDrive} {
		url := g.URLDeAutorizacao("estado", uso)
		if strings.Contains(url, "include_granted_scopes") {
			t.Errorf("uso=%s: o link soma permissões antigas — foi assim que o bot "+
				"ganhou acesso aos 5 TB da diretoria", uso)
		}
	}
}

// A pasta do mês é para GENTE ler, então o nome é em português.
func TestNomeDaPastaDoMes(t *testing.T) {
	casos := map[string]time.Time{
		"Setembro 2026": time.Date(2026, 9, 23, 18, 0, 0, 0, brLocation),
		"Outubro 2026":  time.Date(2026, 10, 1, 9, 0, 0, 0, brLocation),
		"Janeiro 2027":  time.Date(2027, 1, 15, 12, 0, 0, 0, brLocation),
		"Março 2026":    time.Date(2026, 3, 2, 8, 0, 0, 0, brLocation),
	}
	for esperado, quando := range casos {
		if got := NomeDaPastaDoMes(quando); got != esperado {
			t.Errorf("NomeDaPastaDoMes(%v) = %q, esperado %q", quando, got, esperado)
		}
	}

	// A virada do mês em horário de Brasília, não UTC: 30/09 às 22h em SP é
	// 01/10 em UTC, e o arquivo iria para a pasta errada.
	viradaUTC := time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC) // 30/09 22h em SP
	if got := NomeDaPastaDoMes(viradaUTC); got != "Setembro 2026" {
		t.Errorf("virada do mês saiu em UTC: %q (deveria ser Setembro 2026)", got)
	}
}
