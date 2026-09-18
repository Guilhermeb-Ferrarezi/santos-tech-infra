package main

import (
	"strings"
	"testing"
)

// Testes da parte PURA do gerador de currículo, fase 2 (curriculo_prompt.go):
// montagem do brief e parse/validação da resposta do Claude. Nada aqui
// precisa de banco.

func TestCurriculoCampoValido(t *testing.T) {
	for _, campo := range []string{"resumo", "objetivo", "topico", "projeto"} {
		if !curriculoCampoValido(campo) {
			t.Errorf("campo %q deveria ser válido", campo)
		}
	}
	if curriculoCampoValido("qualquer-coisa") {
		t.Error("campo desconhecido não deveria ser válido")
	}
}

func TestMontarBriefCurriculoReescritaContemContexto(t *testing.T) {
	in := briefCurriculoInput{
		Campo:         "topico",
		MaxChars:      130,
		TextoOriginal: "fiz um sistema de vendas",
		Contexto: curriculoContexto{
			ExperienciaCargo:   "Desenvolvedor",
			ExperienciaEmpresa: "Acme Ltda",
			Competencias:       []string{"Python", "SQL"},
		},
	}
	brief := montarBriefCurriculoReescrita(in)
	for _, want := range []string{"Desenvolvedor", "Acme Ltda", "Python", "SQL", "fiz um sistema de vendas", "130 caracteres", "verbo de ação"} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief não contém %q:\n%s", want, brief)
		}
	}
}

// O rascunho da pessoa não pode virar instrução — mesmo sem o delimitador
// dedicado da correção de resposta aberta (aqui o texto é sempre revisado
// por um humano antes de ir pro PDF), o prompt não deve, por si, incentivar
// o modelo a tratar o texto como comando.
func TestMontarBriefCurriculoReescritaSemErroAnteriorNaoMenciona(t *testing.T) {
	brief := montarBriefCurriculoReescrita(briefCurriculoInput{Campo: "resumo", MaxChars: 460, TextoOriginal: "x"})
	if strings.Contains(brief, "ATENÇÃO") {
		t.Error("sem ErroAnterior não deveria mencionar retentativa")
	}
}

func TestMontarBriefCurriculoReescritaComErroAnterior(t *testing.T) {
	brief := montarBriefCurriculoReescrita(briefCurriculoInput{Campo: "resumo", MaxChars: 460, TextoOriginal: "x", ErroAnterior: "JSON inválido: x"})
	if !strings.Contains(brief, "JSON inválido: x") {
		t.Error("deveria repetir o erro anterior no brief")
	}
}

func TestCurriculoCampoPermiteGeracao(t *testing.T) {
	if !curriculoCampoPermiteGeracao("objetivo") {
		t.Error("objetivo deveria permitir geração do zero")
	}
	for _, campo := range []string{"resumo", "topico", "projeto"} {
		if curriculoCampoPermiteGeracao(campo) {
			t.Errorf("%s não deveria permitir geração do zero", campo)
		}
	}
}

// Sem texto original (objetivo de quem nunca trabalhou), o brief muda de
// "reescrever" pra "escrever do zero" e não menciona um texto original que
// não existe — e usa a formação como o principal fato disponível.
func TestMontarBriefCurriculoReescritaGeracaoDoZero(t *testing.T) {
	brief := montarBriefCurriculoReescrita(briefCurriculoInput{
		Campo:         "objetivo",
		MaxChars:      160,
		TextoOriginal: "",
		Contexto: curriculoContexto{
			VagaCargo: "Assistente de TI",
			Formacao:  []string{"Ensino Médio — Escola Pública Tal Tal Tal, cursando"},
		},
	})
	for _, want := range []string{"ESCREVER", "do zero", "PRIMEIRO emprego", "Assistente de TI", "Ensino Médio — Escola Pública Tal Tal Tal, cursando"} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief não contém %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "Texto original") {
		t.Error("sem rascunho, não deveria mencionar \"Texto original\"")
	}
}

func TestParseCurriculoRewriteValido(t *testing.T) {
	out, err := parseCurriculoRewrite(`{"texto": "Desenvolvi um sistema de vendas em Python, reduzindo o tempo de fechamento em 30%."}`, 460)
	if err != nil {
		t.Fatalf("rejeitado: %v", err)
	}
	if out.Texto == "" {
		t.Error("texto vazio")
	}
}

func TestParseCurriculoRewriteCercadoDeTexto(t *testing.T) {
	raw := "Aqui está:\n```json\n" + `{"texto": "Novo texto."}` + "\n```\nEspero que ajude!"
	if _, err := parseCurriculoRewrite(raw, 460); err != nil {
		t.Fatalf("rejeitado: %v", err)
	}
}

func TestParseCurriculoRewriteTrunca(t *testing.T) {
	longo := strings.Repeat("a", 200)
	out, err := parseCurriculoRewrite(`{"texto": "`+longo+`"}`, 50)
	if err != nil {
		t.Fatalf("rejeitado: %v", err)
	}
	if len([]rune(out.Texto)) > 50 {
		t.Errorf("texto não truncado: %d caracteres", len([]rune(out.Texto)))
	}
}

func TestParseCurriculoRewriteInvalido(t *testing.T) {
	cases := map[string]string{
		"sem json":      "não consegui gerar nada",
		"json quebrado": `{"texto": "x"`,
		"texto vazio":   `{"texto": "   "}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseCurriculoRewrite(raw, 460); err == nil {
				t.Fatal("deveria rejeitar")
			}
		})
	}
}

func TestCurriculoMaxCharsClamp(t *testing.T) {
	if got := curriculoMaxCharsClamp(10); got != curriculoMaxCharsMinimo {
		t.Errorf("abaixo do mínimo: got %d want %d", got, curriculoMaxCharsMinimo)
	}
	if got := curriculoMaxCharsClamp(10_000); got != curriculoMaxCharsMaximo {
		t.Errorf("acima do máximo: got %d want %d", got, curriculoMaxCharsMaximo)
	}
	if got := curriculoMaxCharsClamp(200); got != 200 {
		t.Errorf("dentro da faixa deveria manter: got %d", got)
	}
}
