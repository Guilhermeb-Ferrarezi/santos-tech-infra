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
