package main

import (
	"strings"
	"testing"
	"time"
)

// Playbook de raciocínio de venda (spec 2026-09-25-bot-playbook-venda, no repo
// dashboard). Camada 1: princípios de venda, que valem em TODA conversa.

func promptDeTeste(cfg TenantConfig, q Qualificacao) string {
	return BuildPrompt(cfg, ConversationContext{Qualificacao: q}, "oi", time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC))
}

func TestPrincipiosDeVendaEntramEmTodoPrompt(t *testing.T) {
	for _, q := range []Qualificacao{{}, {AlunoIdade: 12}, {AlunoIdade: 40}} {
		p := promptDeTeste(TenantConfig{}, q)
		for _, esperado := range []string{
			"# Como vender",
			"Pergunte mais do que fala",
			"Valor desde a primeira mensagem",
			"Preço por último",
			"Rapport",
			"Rápido e direto",
		} {
			if !strings.Contains(p, esperado) {
				t.Errorf("idade %d: prompt sem %q", q.AlunoIdade, esperado)
			}
		}
	}
}

// A ordem do prompt é a ordem da conversa: primeiro quem é a pessoa e que
// formato serve; depois como conduzir; só então horário.
func TestComoVenderVemDepoisDaModalidadeEAntesDoAgendamento(t *testing.T) {
	p := promptDeTeste(TenantConfig{}, Qualificacao{AlunoIdade: 30})
	modalidade := strings.Index(p, "# Turma ou curso particular")
	vender := strings.Index(p, "# Como vender")
	agenda := strings.Index(p, "# Agendamento de aulas")
	if !(modalidade >= 0 && modalidade < vender && vender < agenda) {
		t.Errorf("ordem errada: modalidade=%d como vender=%d agendamento=%d", modalidade, vender, agenda)
	}
}

func TestPrincipiosPersonalizadosSubstituemOPadrao(t *testing.T) {
	cfg := TenantConfig{RegrasVenda: RegrasVenda{Textos: map[ParteRegra]string{PartePrincipios: "- ESCUTE PRIMEIRO"}}}
	p := promptDeTeste(cfg, Qualificacao{})
	if !strings.Contains(p, "ESCUTE PRIMEIRO") {
		t.Error("princípios da tela não entraram")
	}
	if strings.Contains(p, "Valor desde a primeira mensagem") {
		t.Error("o padrão não pode continuar quando a parte foi reescrita")
	}
}

// A frase "pergunte mais do que fala" morava solta em modalidade.go; agora é
// princípio. Não pode ficar duplicada.
func TestPerguntarMaisDoQueFalarSaiuDaModalidade(t *testing.T) {
	b := Qualificacao{AlunoIdade: 30}.BlocoDaModalidadeCom(RegrasVenda{})
	if strings.Contains(strings.ToLower(b), "pergunte mais do que fala") {
		t.Error("o princípio deveria morar só no bloco Como vender")
	}
}

// ── Seleção das fichas (camada 2) ────────────────────────────────────────────

func ficha(id, titulo string, motivos []string, paraQuem string, idadeHoras int) Situacao {
	return Situacao{
		ID: id, Titulo: titulo, Motivos: motivos, ParaQuem: paraQuem, Estado: "ativa",
		Conduzir:   "conduza",
		AlteradoEm: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC).Add(time.Duration(idadeHoras) * time.Hour),
	}
}

func titulos(ss []Situacao) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.Titulo)
	}
	return out
}

func TestSelecionaSituacoesDossieVazioSoGerais(t *testing.T) {
	ativas := []Situacao{
		ficha("1", "geral velha", nil, "", 1),
		ficha("2", "mercado", []string{"mercado_filho"}, "", 5),
		ficha("3", "só pais", nil, "filho", 6),
		ficha("4", "geral nova", nil, "", 9),
	}
	got := titulos(SelecionaSituacoes(Qualificacao{}, ativas))
	if strings.Join(got, "|") != "geral nova|geral velha" {
		t.Errorf("sem dossiê, só as gerais (mais recente primeiro); veio %v", got)
	}
}

func TestSelecionaSituacoesCasaMotivoEParaQuemAntesDasGerais(t *testing.T) {
	ativas := []Situacao{
		ficha("1", "geral", nil, "", 9),
		ficha("2", "mercado", []string{"mercado_filho", "emprego"}, "", 1),
		ficha("3", "só pais", nil, "filho", 2),
		ficha("4", "mercado de pais", []string{"mercado_filho"}, "filho", 0),
		ficha("5", "emprego próprio", []string{"emprego"}, "proprio", 3),
	}
	q := Qualificacao{ParaQuem: "filho", MotivacaoTipo: "mercado_filho"}
	got := titulos(SelecionaSituacoes(q, ativas))
	if strings.Join(got, "|") != "mercado de pais|mercado|só pais|geral" {
		t.Errorf("ordem esperada: motivo+para quem, motivo, para quem, geral; veio %v", got)
	}
}

func TestSelecionaSituacoesNuncaPegaRascunhoOuArquivada(t *testing.T) {
	r := ficha("1", "rascunho", nil, "", 1)
	r.Estado = "rascunho"
	a := ficha("2", "arquivada", nil, "", 1)
	a.Estado = "arquivada"
	if got := SelecionaSituacoes(Qualificacao{}, []Situacao{r, a}); len(got) != 0 {
		t.Errorf("rascunho e arquivada nunca entram; veio %v", titulos(got))
	}
}

func TestSelecionaSituacoesRespeitaOsTetos(t *testing.T) {
	var muitas []Situacao
	for i := 0; i < 20; i++ {
		muitas = append(muitas, ficha(string(rune('a'+i)), "ficha", nil, "", i))
	}
	if got := SelecionaSituacoes(Qualificacao{}, muitas); len(got) != maxSituacoesNoPrompt {
		t.Errorf("teto de fichas: esperado %d, veio %d", maxSituacoesNoPrompt, len(got))
	}
	grandes := []Situacao{}
	for i := 0; i < 8; i++ {
		s := ficha(string(rune('a'+i)), "grande", nil, "", i)
		s.Conduzir = strings.Repeat("x", 1400)
		s.PorTras = strings.Repeat("y", 1400)
		grandes = append(grandes, s)
	}
	got := SelecionaSituacoes(Qualificacao{}, grandes)
	total := 0
	for i, s := range got {
		total += len([]rune(textoDaSituacao(i, s)))
	}
	if total > maxCaracteresSituacoes || len(got) == 0 {
		t.Errorf("teto de caracteres: %d fichas, %d caracteres (teto %d)", len(got), total, maxCaracteresSituacoes)
	}
}

// ── Fichas no prompt e o que o modelo diz que usou ───────────────────────────

func TestFichasEscolhidasEntramNoPromptComIdCurto(t *testing.T) {
	cfg := TenantConfig{Situacoes: []Situacao{
		{ID: "uuid-a", Titulo: "Gostou, mas vai espaçar", Sinais: "vou espaçar", Conduzir: "registre o retorno", Estado: "ativa"},
		{ID: "uuid-b", Titulo: "Mãe preocupada com tela", Conduzir: "pergunte o que ele joga", Estado: "ativa"},
	}}
	p := promptDeTeste(cfg, Qualificacao{})
	for _, esperado := range []string{
		"## Situações que você pode reconhecer",
		"### [s1] Gostou, mas vai espaçar",
		"Quando reconhecer: vou espaçar",
		"### [s2] Mãe preocupada com tela",
		"\"situacoesUsadas\"",
	} {
		if !strings.Contains(p, esperado) {
			t.Errorf("prompt sem %q", esperado)
		}
	}
	if strings.Contains(p, "uuid-a") {
		t.Error("o uuid não vai ao prompt, só o id curto")
	}
	if sem := promptDeTeste(TenantConfig{}, Qualificacao{}); strings.Contains(sem, "## Situações que você pode reconhecer") {
		t.Error("sem ficha ativa, o bloco de situações não aparece")
	}
}

// O modelo só pode "ter usado" o que estava no prompt: id inventado, repetido
// ou fora da lista é descartado. Um número de uso que o modelo infla sozinho
// não mede nada.
func TestIdsUsadosSoValemOsQueEstavamNoPrompt(t *testing.T) {
	escolhidas := []Situacao{{ID: "uuid-a"}, {ID: "uuid-b"}}
	got := uuidsDasSituacoesUsadas(escolhidas, []string{"s2", "s9", " S1 ", "s2", "uuid-a", ""})
	if strings.Join(got, ",") != "uuid-b,uuid-a" {
		t.Errorf("esperado uuid-b,uuid-a — veio %v", got)
	}
	if got := uuidsDasSituacoesUsadas(nil, []string{"s1"}); len(got) != 0 {
		t.Errorf("sem ficha no prompt nada conta; veio %v", got)
	}
}

func TestParserLeSituacoesUsadas(t *testing.T) {
	out, err := ParseModelReply(`{"bubbles":["oi"],"answered":true,"answeredFromKb":false,"situacoesUsadas":["s1"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.SituacoesUsadas) != 1 || out.SituacoesUsadas[0] != "s1" {
		t.Errorf("situacoesUsadas não lido: %v", out.SituacoesUsadas)
	}
}
