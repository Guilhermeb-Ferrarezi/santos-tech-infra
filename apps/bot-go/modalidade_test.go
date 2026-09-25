package main

import (
	"strings"
	"testing"
	"time"
)

// A decisão entre turma e curso particular sai da IDADE (calculada aqui, em Go)
// e depois do interesse e da disponibilidade (julgados pelo modelo com a KB).
// Regra do Henrique, 25/09/2026.

func TestModalidadeSemIdadeMandaPerguntarAntes(t *testing.T) {
	b := Qualificacao{}.BlocoDaModalidade()
	if !strings.Contains(b, "Ainda não sei a idade") {
		t.Errorf("sem idade, o bloco deveria mandar descobrir a idade antes de indicar formato:\n%s", b)
	}
}

func TestModalidadeDezessetePraCimaEParticular(t *testing.T) {
	for _, idade := range []int{17, 18, 34, 70} {
		b := Qualificacao{AlunoIdade: idade}.BlocoDaModalidade()
		if !strings.Contains(b, "público do CURSO PARTICULAR") {
			t.Errorf("%d anos deveria ir direto pro particular:\n%s", idade, b)
		}
		if !strings.Contains(b, "NÃO ofereça turma") {
			t.Errorf("%d anos não pode receber oferta de turma", idade)
		}
	}
}

func TestModalidadeQuinzeDezesseisEZonaDeConversa(t *testing.T) {
	for _, idade := range []int{15, 16} {
		b := Qualificacao{AlunoIdade: idade}.BlocoDaModalidade()
		if !strings.Contains(b, "fim da faixa dos programas") {
			t.Errorf("%d anos deveria cair na zona de conversa:\n%s", idade, b)
		}
	}
}

func TestModalidadeCriancaDecidePeloInteresse(t *testing.T) {
	b := Qualificacao{AlunoIdade: 12}.BlocoDaModalidade()
	if !strings.Contains(b, "idade de turma") {
		t.Errorf("12 anos é idade de turma:\n%s", b)
	}
	if strings.Contains(b, "NÃO ofereça turma") {
		t.Error("12 anos não pode ter a turma vetada pela idade")
	}
}

// O vocabulário vale em toda conversa, com ou sem idade.
func TestModalidadeFixaVocabularioParticular(t *testing.T) {
	b := Qualificacao{}.BlocoDaModalidade()
	for _, esperado := range []string{"CURSO PARTICULAR", "curso de adulto", "Base de Conhecimento"} {
		if !strings.Contains(b, esperado) {
			t.Errorf("bloco sem %q:\n%s", esperado, b)
		}
	}
}

// Os motivos internos da escola orientam o bot, mas não podem virar fala.
func TestModalidadeMotivoInternoMarcadoComoNaoFalar(t *testing.T) {
	b := Qualificacao{}.BlocoDaModalidade()
	if !strings.Contains(b, "NUNCA diga ao cliente") {
		t.Errorf("os motivos internos precisam vir marcados como não-falar:\n%s", b)
	}
}

// Nenhuma instrução fixa do prompt pode chamar o produto de "de adulto".
func TestPromptNaoChamaProdutoDeAdulto(t *testing.T) {
	cfg := TenantConfig{}
	p := BuildPrompt(cfg, ConversationContext{}, "oi", time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC))
	for _, proibido := range []string{"INDIVIDUAL de adulto", "particular de adulto"} {
		if strings.Contains(p, proibido) {
			t.Errorf("prompt ainda contém %q", proibido)
		}
	}
	if !strings.Contains(p, "# Turma ou curso particular") {
		t.Error("prompt sem o bloco de modalidade")
	}
}

// A idade vale pra todo mundo: é ela que separa o adolescente de 16 anos
// escrevendo por conta própria do adulto de 30.
func TestIdadePerguntadaTambemParaQuemEhOProprioAluno(t *testing.T) {
	q := Qualificacao{}.Merge(Qualificacao{ParaQuem: "proprio", Interesse: "Excel"})
	achou := false
	for _, p := range q.Falta() {
		if p.campo == "alunoIdade" {
			achou = true
			if !strings.Contains(p.texto(q), "sua idade") {
				t.Errorf("pra quem é o próprio aluno a pergunta deve ser na 2ª pessoa, veio %q", p.texto(q))
			}
		}
	}
	if !achou {
		t.Error("a idade precisa ser perguntada também quando o curso é pra própria pessoa")
	}
}

// Ancoragem do particular (Henrique, 25/09/2026): muita gente carrega a imagem
// de que aprender é numa sala cheia. Ao apresentar o particular, o bot ancora
// em "é o MESMO curso da turma" e mostra o que muda — sem citar tamanho de
// turma e sem diminuir a turma, que é o produto principal para criança.
func TestModalidadeAncoraParticularNoMesmoCurso(t *testing.T) {
	b := Qualificacao{}.BlocoDaModalidade()
	for _, esperado := range []string{
		"Como apresentar o curso particular",
		"é o MESMO curso",
		"se adapta",
		"NÃO diminua a turma",
		"NÃO cite número de alunos",
	} {
		if !strings.Contains(b, esperado) {
			t.Errorf("bloco sem %q:\n%s", esperado, b)
		}
	}
}

// "professor ou professora" é redundância: o Henrique pediu para eliminar.
func TestModalidadeSemRedundanciaProfessorOuProfessora(t *testing.T) {
	b := Qualificacao{}.BlocoDaModalidade()
	if !strings.Contains(b, "NÃO escreva \"professor ou professora\"") {
		t.Error("o bloco deveria proibir a redundância \"professor ou professora\"")
	}
}

// Henrique, 25/09/2026: quando a turma NÃO é opção (17+), a alternativa real do
// cliente é uma turma em outra escola — aí o contraste com a turma trabalha a
// favor, e o bot pode mostrar com franqueza por que ela serve menos a ele.
// Quando o aluno tem idade de turma, a turma é produto nosso: nunca diminuir.
func TestModalidadeContrasteComTurmaSoQuandoTurmaNaoEOpcao(t *testing.T) {
	adulto := Qualificacao{AlunoIdade: 45}.BlocoDaModalidade()
	if !strings.Contains(adulto, "pode mostrar com franqueza por que a turma serve menos") {
		t.Errorf("45 anos: o contraste com turma deveria estar liberado:\n%s", adulto)
	}
	if strings.Contains(adulto, "NÃO diminua a turma") {
		t.Error("45 anos: a proibição de diminuir a turma não se aplica")
	}
	if !strings.Contains(adulto, "NUNCA fale mal de uma escola específica") {
		t.Error("o contraste é com o formato turma, nunca com um concorrente nomeado")
	}

	for _, q := range []Qualificacao{{AlunoIdade: 12}, {AlunoIdade: 16}, {}} {
		b := q.BlocoDaModalidade()
		if !strings.Contains(b, "NÃO diminua a turma") {
			t.Errorf("idade %d: a turma é produto nosso e não pode ser diminuída", q.AlunoIdade)
		}
		if strings.Contains(b, "pode mostrar com franqueza") {
			t.Errorf("idade %d: contraste com turma não pode estar liberado", q.AlunoIdade)
		}
	}
}
