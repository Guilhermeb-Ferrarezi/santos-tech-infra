package main

import "testing"

// O e-mail é a identidade do aluno e não muda depois. Estes casos são o
// contrato combinado com o Rodrigo em set/2026: primeiro e último nome,
// separados por ponto, sem acento.
func TestLocalPartAluno(t *testing.T) {
	casos := []struct{ nome, quer string }{
		{"Enzo Silva", "enzo.silva"},
		// O meio do nome é descartado de propósito.
		{"José Gonçalves de Assunção", "jose.assuncao"},
		{"João D'Ávila", "joao.davila"},
		{"MARIA CLARA SOUZA", "maria.souza"},
		{"  Ana   Paula  Lima  ", "ana.lima"},
		// Nome único: fica só ele, sem ponto solto no fim.
		{"Madonna", "madonna"},
		{"Ção", "cao"},
		{"", ""},
		// Só pontuação não vira e-mail — o handler recusa antes de criar nada.
		{"...", ""},
	}
	for _, c := range casos {
		if got := localPartAluno(c.nome); got != c.quer {
			t.Errorf("localPartAluno(%q) = %q, quer %q", c.nome, got, c.quer)
		}
	}
}

func TestLocalPartTentativa(t *testing.T) {
	casos := []struct {
		tentativa int
		quer      string
	}{
		// O primeiro fica com o endereço limpo.
		{1, "enzo.silva"},
		{2, "enzo.silva.002"},
		{10, "enzo.silva.010"},
		{999, "enzo.silva.999"},
	}
	for _, c := range casos {
		if got := localPartTentativa("enzo.silva", c.tentativa); got != c.quer {
			t.Errorf("localPartTentativa(%d) = %q, quer %q", c.tentativa, got, c.quer)
		}
	}
}

// Todo endereço gerado tem de passar na validação que o próprio auth aplica —
// senão o cadastro morre no meio, com a conta já meio criada.
func TestLocalPartAlunoPassaNaValidacaoDoAuth(t *testing.T) {
	for _, nome := range []string{"Enzo Silva", "José Gonçalves de Assunção", "MARIA CLARA SOUZA", "Madonna"} {
		base := localPartAluno(nome)
		if !localPartRe.MatchString(base) {
			t.Errorf("localPartAluno(%q) = %q não passa em localPartRe", nome, base)
		}
		if !localPartRe.MatchString(localPartTentativa(base, 2)) {
			t.Errorf("a variante .002 de %q não passa em localPartRe", base)
		}
	}
}
