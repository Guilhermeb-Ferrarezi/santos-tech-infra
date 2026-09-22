package main

import "testing"

func TestParseQuizBlockRotulosComParenteses(t *testing.T) {
	raw := "Qual a capital da Mongólia?\nA) Astana\nB) Ulan Bator\nC) Bishkek"
	got, err := parseQuizBlock(raw)
	if err != nil {
		t.Fatalf("parseQuizBlock: %v", err)
	}
	if got.Question != "Qual a capital da Mongólia?" {
		t.Errorf("enunciado = %q", got.Question)
	}
	if len(got.Options) != 3 || got.Options["B"] != "Ulan Bator" {
		t.Errorf("alternativas = %v", got.Options)
	}
	if len(got.Order) != 3 || got.Order[0] != "A" || got.Order[2] != "C" {
		t.Errorf("ordem = %v", got.Order)
	}
}

func TestParseQuizBlockPreservaRotuloNumerico(t *testing.T) {
	raw := "Quanto é 2+2?\n1. Três\n2. Quatro\n3. Cinco"
	got, err := parseQuizBlock(raw)
	if err != nil {
		t.Fatalf("parseQuizBlock: %v", err)
	}
	// O rótulo devolvido tem que ser o da prova, não uma letra inventada:
	// a pessoa marca "2" no cartão, não "B".
	if got.Options["2"] != "Quatro" {
		t.Errorf("alternativas = %v", got.Options)
	}
}

func TestParseQuizBlockAlternativaDeVariasLinhas(t *testing.T) {
	raw := "Questão longa?\nA) primeira parte\ncontinuação da A\nB) segunda"
	got, err := parseQuizBlock(raw)
	if err != nil {
		t.Fatalf("parseQuizBlock: %v", err)
	}
	if got.Options["A"] != "primeira parte continuação da A" {
		t.Errorf("A = %q", got.Options["A"])
	}
}

func TestParseQuizBlockEnunciadoDeVariasLinhas(t *testing.T) {
	raw := "Considere o texto abaixo.\nEle descreve um caso.\n(a) certo\n(b) errado"
	got, err := parseQuizBlock(raw)
	if err != nil {
		t.Fatalf("parseQuizBlock: %v", err)
	}
	if got.Question != "Considere o texto abaixo. Ele descreve um caso." {
		t.Errorf("enunciado = %q", got.Question)
	}
	if got.Options["a"] != "certo" {
		t.Errorf("alternativas = %v", got.Options)
	}
}

func TestParseQuizBlockSemRotuloUsaPergunta(t *testing.T) {
	raw := "Qual é a cor do céu?\nAzul\nVerde\nVermelho"
	got, err := parseQuizBlock(raw)
	if err != nil {
		t.Fatalf("parseQuizBlock: %v", err)
	}
	if got.Question != "Qual é a cor do céu?" {
		t.Errorf("enunciado = %q", got.Question)
	}
	if len(got.Options) != 3 || got.Options["A"] != "Azul" {
		t.Errorf("alternativas geradas = %v", got.Options)
	}
}

func TestParseQuizBlockPoucasAlternativas(t *testing.T) {
	if _, err := parseQuizBlock("Só um texto solto sem alternativa nenhuma"); err != errQuizUnparseable {
		t.Errorf("err = %v, queria errQuizUnparseable", err)
	}
}

func TestParseQuizBlockAlternativasDemais(t *testing.T) {
	raw := "Pergunta?\n1. a\n2. b\n3. c\n4. d\n5. e\n6. f\n7. g\n8. h\n9. i\n1. j"
	if _, err := parseQuizBlock(raw); err != errQuizUnparseable {
		t.Errorf("err = %v, queria errQuizUnparseable (rótulo repetido/acima do limite)", err)
	}
}
