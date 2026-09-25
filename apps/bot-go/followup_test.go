package main

import (
	"strings"
	"testing"
)

// A ordem das fontes é a razão de a coluna origem_fonte existir.
//
// O caso real: a pessoa chega por um anúncio do Instagram, e três mensagens
// depois o bot pergunta e ela responde "achei no Google" — porque ninguém lembra
// por onde clicou. Sem a ordem, a lembrança sobrescreveria o fato.
func TestLembrancaNaoSobrescreveOAnuncio(t *testing.T) {
	q := Qualificacao{}.comOrigem("anuncio_instagram", "anúncio \"Férias\" id 120", "anuncio")
	if q.Origem != "anuncio_instagram" {
		t.Fatalf("o anúncio não foi gravado: %+v", q)
	}

	depois := q.comOrigem("google", "a pessoa contou na conversa", "perguntado")
	if depois.Origem != "anuncio_instagram" {
		t.Errorf("a lembrança do cliente apagou o fato da Meta: %q", depois.Origem)
	}
	if !strings.Contains(depois.OrigemDetalhe, "Férias") {
		t.Errorf("o detalhe do anúncio se perdeu: %q", depois.OrigemDetalhe)
	}
}

// E o contrário tem que funcionar: quando só havia a lembrança, o anúncio corrige.
func TestAnuncioCorrigeALembranca(t *testing.T) {
	q := Qualificacao{}.comOrigem("google", "contou na conversa", "perguntado")
	q = q.comOrigem("anuncio_facebook", "anúncio id 9", "anuncio")
	if q.Origem != "anuncio_facebook" || q.OrigemFonte != "anuncio" {
		t.Errorf("o fato não corrigiu o palpite: %q (%s)", q.Origem, q.OrigemFonte)
	}
}

// Origem fora do vocabulário não entra. Uma grafia solta contamina todo
// relatório que vier depois, e não há como limpar retroativamente.
func TestOrigemForaDoVocabularioNaoEntra(t *testing.T) {
	q := Qualificacao{}.comOrigem("tiktok", "x", "perguntado")
	if q.Origem != "" {
		t.Errorf("gravou origem inválida: %q", q.Origem)
	}
}

// O anúncio só fala uma vez, na primeira mensagem. Se o engine não aproveitar
// ali, a informação some — não há como pedir de novo à Meta.
func TestOrigemDaChegadaPrefereOAnuncio(t *testing.T) {
	cfg := TenantConfig{OrigemMarcadores: []MarcadorOrigem{
		{Marcador: "vim pelo site", Origem: "site"},
	}}
	inbound := InboundMessage{
		Origem:        "anuncio_instagram",
		OrigemDetalhe: "anúncio id 1",
		Content:       MessageContent{Type: "text", Text: "vim pelo site"},
	}
	q := origemDaChegada(Qualificacao{}, inbound, cfg, true)
	if q.Origem != "anuncio_instagram" {
		t.Errorf("com anúncio E marcador, tem que valer o anúncio: %q", q.Origem)
	}
}

// O marcador só vale na primeira mensagem: "vim pelo site" dito no meio de uma
// conversa é assunto, não procedência.
func TestMarcadorSoValeNaPrimeiraMensagem(t *testing.T) {
	cfg := TenantConfig{OrigemMarcadores: []MarcadorOrigem{
		{Marcador: "vim pelo site", Origem: "site"},
	}}
	inbound := InboundMessage{Content: MessageContent{Type: "text", Text: "vim pelo site"}}

	if q := origemDaChegada(Qualificacao{}, inbound, cfg, true); q.Origem != "site" {
		t.Errorf("primeira mensagem: esperava site, veio %q", q.Origem)
	}
	if q := origemDaChegada(Qualificacao{}, inbound, cfg, false); q.Origem != "" {
		t.Errorf("no meio da conversa não podia gravar origem: %q", q.Origem)
	}
}

// Quem chega por áudio também tem marcador para ler — a transcrição é o texto.
func TestMarcadorFuncionaNoAudio(t *testing.T) {
	cfg := TenantConfig{OrigemMarcadores: []MarcadorOrigem{
		{Marcador: "vim pelo Google", Origem: "google"},
	}}
	tr := "oi, vim pelo Google, queria saber dos cursos"
	inbound := InboundMessage{Content: MessageContent{Type: "audio", Transcript: &tr}}
	if q := origemDaChegada(Qualificacao{}, inbound, cfg, true); q.Origem != "google" {
		t.Errorf("não leu a transcrição: %q", q.Origem)
	}
}

// A pergunta de origem não pode virar a sétima pergunta de um formulário: ela só
// aparece depois que a qualificação andou, e some assim que a origem é sabida.
func TestPerguntaDeOrigemSoApareceNaHoraCerta(t *testing.T) {
	cedo := Qualificacao{ParaQuem: "filho"} // 1 respondida
	if strings.Contains(cedo.BlocoDasRegras(), "como você chegou") {
		t.Error("pediu a origem cedo demais — vira interrogatório")
	}

	naHora := Qualificacao{ParaQuem: "filho", AlunoIdade: 9, Interesse: "jogos"}
	if !strings.Contains(naHora.BlocoDasRegras(), "como você chegou") {
		t.Error("nunca pede a origem; ela só seria conhecida por anúncio")
	}

	jaSabe := naHora
	jaSabe.Origem = "google"
	if strings.Contains(jaSabe.BlocoDasRegras(), "como você chegou") {
		t.Error("pediu de novo uma origem que já se sabe — e ainda custa token em toda mensagem")
	}
}

// A origem conhecida entra no dossiê para o bot NÃO perguntar de novo, com o
// aviso de não comentar: "vi que você veio do nosso anúncio" assusta o cliente.
func TestOrigemNoDossieMandaNaoComentar(t *testing.T) {
	q := Qualificacao{Origem: "anuncio_instagram"}
	d := q.BlocoDoDossie()
	if !strings.Contains(d, "anúncio no Instagram") {
		t.Errorf("a origem não aparece no dossiê:\n%s", d)
	}
	if !strings.Contains(d, "não comente") {
		t.Error("o dossiê não avisa para não comentar a origem com o cliente")
	}
}

// ── follow-up ────────────────────────────────────────────────────────────────

// O resumo é a conta que a escola quer ver. Aula sem resultado NÃO pode contar
// como "não fechou": seria inventar fracasso onde só há trabalho pendente.
func TestResumoSeparaSemResultadoDeNaoFechou(t *testing.T) {
	r := Resume([]LinhaFollowup{
		{Resultado: ""},
		{Resultado: "faltou"},
		{Resultado: "veio"},
		{Resultado: "fechou", Origem: "anuncio_instagram"},
		{Resultado: "fechou", Origem: "indicacao"},
		{Resultado: "fechou", Origem: ""},
		{Resultado: "nao_fechou"},
	})
	if r.Total != 7 {
		t.Errorf("total errado: %d", r.Total)
	}
	if r.SemResultado != 1 {
		t.Errorf("sem resultado: esperava 1, veio %d", r.SemResultado)
	}
	if r.NaoFechou != 1 {
		t.Errorf("não fechou: esperava 1, veio %d — aula sem marcação virou fracasso?", r.NaoFechou)
	}
	if r.Fechou != 3 {
		t.Errorf("fechou: esperava 3, veio %d", r.Fechou)
	}
	if r.PorOrigem["anuncio_instagram"] != 1 || r.PorOrigem["indicacao"] != 1 {
		t.Errorf("conversão por origem errada: %v", r.PorOrigem)
	}
	// Fechou sem origem conhecida não pode sumir da conta: some do relatório e
	// a soma por origem deixa de bater com o total de fechados.
	if r.PorOrigem["desconhecida"] != 1 {
		t.Errorf("fechado sem origem desapareceu: %v", r.PorOrigem)
	}
}

func TestVocabularioDeResultadoEFechado(t *testing.T) {
	for _, bom := range []string{"faltou", "veio", "fechou", "nao_fechou"} {
		if !ResultadoValido(bom) {
			t.Errorf("%q devia ser aceito", bom)
		}
		if ResultadoLegivel(bom) == "" {
			t.Errorf("%q não tem tradução para o painel", bom)
		}
	}
	for _, ruim := range []string{"", "compareceu", "FECHOU", "veio_e_fechou"} {
		if ResultadoValido(ruim) {
			t.Errorf("%q não devia ser aceito", ruim)
		}
	}
}

func TestStatusDeAvaliacaoEFechado(t *testing.T) {
	for _, bom := range []string{"nao_pedido", "pedido", "avaliou", "recusou", "nao_pedir"} {
		if !StatusAvaliacaoValido(bom) {
			t.Errorf("%q devia ser aceito", bom)
		}
	}
	if StatusAvaliacaoValido("avaliado") {
		t.Error("aceitou status inventado")
	}
	// Quem nunca teve linha aparece como "ainda não convidado", e não em branco:
	// a fila de trabalho precisa mostrar essa pessoa.
	if StatusAvaliacaoLegivel("") == "" {
		t.Error("status vazio ficou sem rótulo; some da fila de convites")
	}
}
