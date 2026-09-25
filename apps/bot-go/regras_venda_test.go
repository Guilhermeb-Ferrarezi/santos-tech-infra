package main

import (
	"strings"
	"testing"
	"time"
)

// Regras de venda como dados (spec 2026-09-25-bot-regras-venda-editaveis).
// O texto é do Henrique e muda pela tela; a mecânica (idade, faixa, travas,
// linha da oportunidade de turma) continua no código e não pode sumir quando
// ele reescrever uma parte.

// paraEstaPessoa devolve só o trecho calculado para a pessoa — é ali que a
// indicação de ESTA conversa aparece; o resto do bloco lista todas as faixas.
func paraEstaPessoa(bloco string) string {
	i := strings.Index(bloco, "Para esta pessoa:")
	if i < 0 {
		return ""
	}
	return bloco[i:]
}

func TestRegrasPadraoContinuamNoPrompt(t *testing.T) {
	b := Qualificacao{AlunoIdade: 45}.BlocoDaModalidadeCom(RegrasVenda{})
	for _, esperado := range []string{
		"# Turma ou curso particular",
		"é o MESMO curso",
		"CURSO PARTICULAR",
		"Oportunidade de turma nova:",
		"NUNCA fale mal de uma escola específica",
		"NUNCA diga ao cliente os motivos da ESCOLA",
		"Só afirme que não há turma no horário",
		"Para esta pessoa:",
		"NÃO escreva \"curso de adulto\"",
	} {
		if !strings.Contains(b, esperado) {
			t.Errorf("bloco padrão sem %q:\n%s", esperado, b)
		}
	}
}

// Reescrever uma parte troca só ela: faixas, travas, a linha da oportunidade
// e a indicação da pessoa continuam lá.
func TestPartePersonalizadaSubstituiSoEla(t *testing.T) {
	r := RegrasVenda{Textos: map[ParteRegra]string{
		ParteApresentarParticular: "- TEXTO NOVO DO HENRIQUE",
	}}
	b := Qualificacao{AlunoIdade: 30}.BlocoDaModalidadeCom(r)
	if !strings.Contains(b, "TEXTO NOVO DO HENRIQUE") {
		t.Errorf("parte personalizada não entrou:\n%s", b)
	}
	if strings.Contains(b, "ANCORE nela") {
		t.Error("o texto padrão da parte trocada não pode continuar no bloco")
	}
	for _, esperado := range []string{
		"Faixas de idade",
		"Oportunidade de turma nova:",
		"NUNCA fale mal de uma escola específica",
		"Para esta pessoa:",
		"público do CURSO PARTICULAR",
	} {
		if !strings.Contains(b, esperado) {
			t.Errorf("a troca de uma parte levou junto %q:\n%s", esperado, b)
		}
	}
}

func TestParteVaziaOuSoEspacosUsaOPadrao(t *testing.T) {
	padrao := Qualificacao{AlunoIdade: 12}.BlocoDaModalidadeCom(RegrasVenda{})
	for _, vazio := range []string{"", "   ", "\n\t "} {
		r := RegrasVenda{Textos: map[ParteRegra]string{ParteApresentarParticular: vazio, ParteDecidir: vazio}}
		if b := (Qualificacao{AlunoIdade: 12}).BlocoDaModalidadeCom(r); b != padrao {
			t.Errorf("parte %q deveria cair no padrão", vazio)
		}
	}
}

func TestFaixasDeIdadeConfiguraveis(t *testing.T) {
	// 18/15: quem tem 17 ainda cabe em turma (fim da faixa).
	r := RegrasVenda{Faixas: FaixasIdade{IdadeParticular: 18, IdadeFimFaixaTurma: 15}}
	b := paraEstaPessoa(Qualificacao{AlunoIdade: 17}.BlocoDaModalidadeCom(r))
	if !strings.Contains(b, "fim da faixa dos programas") {
		t.Errorf("com particular a partir de 18, 17 anos é fim da faixa:\n%s", b)
	}
	todo := Qualificacao{AlunoIdade: 17}.BlocoDaModalidadeCom(r)
	if strings.Contains(todo, "pode mostrar com franqueza") {
		t.Error("com particular a partir de 18, 17 anos ainda é idade de turma: nada de contraste")
	}
	if !strings.Contains(todo, "18 anos ou mais") || !strings.Contains(todo, "15 a 17 anos") || !strings.Contains(todo, "até 14 anos") {
		t.Errorf("a lista de faixas precisa refletir os números da tela:\n%s", todo)
	}

	// Padrão 17/15: 17 anos é particular, com contraste liberado.
	todo = Qualificacao{AlunoIdade: 17}.BlocoDaModalidadeCom(RegrasVenda{})
	if !strings.Contains(paraEstaPessoa(todo), "público do CURSO PARTICULAR") {
		t.Errorf("17 anos no padrão é particular:\n%s", todo)
	}
	if !strings.Contains(todo, "pode mostrar com franqueza") {
		t.Error("17 anos no padrão: contraste com turma liberado")
	}
}

// Pega o próximo "adulto" esquecido numa string do código: nenhum termo da
// lista "evitar" pode aparecer no prompt fora da própria linha que o proíbe.
func TestNenhumTermoEvitadoNoCodigoDoPrompt(t *testing.T) {
	cfg := TenantConfig{}
	for _, idade := range idadesDoOuro {
		ctx := ConversationContext{Qualificacao: Qualificacao{AlunoIdade: idade}}
		p := BuildPrompt(cfg, ctx, "oi", time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC))
		var semVocabulario []string
		for _, l := range strings.Split(p, "\n") {
			if strings.HasPrefix(l, prefixoLinhaVocabulario) {
				continue
			}
			semVocabulario = append(semVocabulario, l)
		}
		resto := strings.ToLower(strings.Join(semVocabulario, "\n"))
		for _, termo := range PadraoRegrasVenda().Vocabulario {
			if strings.Contains(resto, strings.ToLower(termo.Evitar)) {
				t.Errorf("%d anos: o prompt usa %q fora da linha de vocabulário", idade, termo.Evitar)
			}
		}
	}
}

func TestResolvidaUsaPadraoNoQueNaoFoiPreenchido(t *testing.T) {
	r := RegrasVenda{}.Resolvida()
	p := PadraoRegrasVenda()
	if r.Faixas != p.Faixas {
		t.Errorf("faixas zeradas deveriam virar o padrão, veio %+v", r.Faixas)
	}
	if len(r.Vocabulario) != len(p.Vocabulario) {
		t.Error("vocabulário vazio deveria virar o padrão")
	}
	for _, parte := range PartesDasRegras {
		if strings.TrimSpace(r.Textos[parte]) == "" {
			t.Errorf("parte %s sem texto depois de resolver", parte)
		}
	}
}

func TestValidaRegras(t *testing.T) {
	grande := strings.Repeat("a", maxTextoRegra+1)
	muitos := make([]TermoVocabulario, maxTermosVocabulario+1)
	for i := range muitos {
		muitos[i] = TermoVocabulario{Evitar: "x", Usar: "y"}
	}
	casos := map[string]RegrasVenda{
		"faixa invertida":      {Faixas: FaixasIdade{IdadeParticular: 14, IdadeFimFaixaTurma: 15}},
		"faixa igual":          {Faixas: FaixasIdade{IdadeParticular: 15, IdadeFimFaixaTurma: 15}},
		"idade baixa demais":   {Faixas: FaixasIdade{IdadeParticular: 17, IdadeFimFaixaTurma: 4}},
		"idade alta demais":    {Faixas: FaixasIdade{IdadeParticular: 100, IdadeFimFaixaTurma: 15}},
		"texto grande":         {Textos: map[ParteRegra]string{ParteDecidir: grande}},
		"parte desconhecida":   {Textos: map[ParteRegra]string{"inventada": "x"}},
		"vocabulário demais":   {Vocabulario: muitos},
		"termo vazio":          {Vocabulario: []TermoVocabulario{{Evitar: "  ", Usar: "x"}}},
		"termo grande":         {Vocabulario: []TermoVocabulario{{Evitar: strings.Repeat("a", maxTamanhoTermo+1)}}},
		"só uma faixa mudada ": {Faixas: FaixasIdade{IdadeFimFaixaTurma: 17}}, // 17 com particular padrão 17
	}
	for nome, r := range casos {
		if err := r.Valida(); err == nil {
			t.Errorf("%s: deveria ser recusado", nome)
		}
	}
	validos := []RegrasVenda{
		{},
		{Faixas: FaixasIdade{IdadeParticular: 18, IdadeFimFaixaTurma: 15}},
		{Faixas: FaixasIdade{IdadeParticular: 18}},
		{Vocabulario: []TermoVocabulario{{Evitar: "curso de adulto"}}},
		PadraoRegrasVenda(),
	}
	for i, r := range validos {
		if err := r.Valida(); err != nil {
			t.Errorf("válido %d recusado: %v", i, err)
		}
	}
}

// Salvar o que é igual ao padrão grava vazio: assim, quando o padrão melhorar
// no código, a melhoria chega a quem não personalizou.
func TestSemPadraoGuardaVazioOQueEIgualAoPadrao(t *testing.T) {
	p := PadraoRegrasVenda()
	r := p
	r.Textos = map[ParteRegra]string{}
	for k, v := range p.Textos {
		r.Textos[k] = "  " + v + "\n"
	}
	r.Textos[ParteDecidir] = "meu texto"
	s := r.SemPadrao()
	if s.Faixas != (FaixasIdade{}) {
		t.Errorf("faixas iguais ao padrão deveriam virar zero, veio %+v", s.Faixas)
	}
	if s.Vocabulario != nil {
		t.Error("vocabulário igual ao padrão deveria virar vazio")
	}
	for _, parte := range PartesDasRegras {
		want := ""
		if parte == ParteDecidir {
			want = "meu texto"
		}
		if s.Textos[parte] != want {
			t.Errorf("%s: esperado %q, veio %q", parte, want, s.Textos[parte])
		}
	}
}
