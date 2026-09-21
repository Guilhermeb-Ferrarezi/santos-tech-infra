package main

import (
	"strings"
	"testing"
	"time"
)

// As três mensagens têm propósitos diferentes — confirmar, lembrar, receber.
// Se virarem variações da mesma frase, o cliente aprende a ignorar a terceira.
func TestOsTresLembretesDizemCoisasDiferentes(t *testing.T) {
	quer := map[TipoLembrete][]string{
		LembreteVespera:     {"confirm"},
		LembreteQuatroHoras: {"lembr"},
		LembreteUmaHora:     {"Verônica"},
	}
	for kind, termos := range quer {
		variantes := textosLembrete[kind]
		if len(variantes) < 2 {
			t.Errorf("%s tem %d variante(s); sem sorteio duas famílias recebem a mesma frase", kind, len(variantes))
		}
		for _, v := range variantes {
			achou := false
			for _, termo := range termos {
				if strings.Contains(strings.ToLower(v), strings.ToLower(termo)) {
					achou = true
				}
			}
			if !achou {
				t.Errorf("variante de %s não cumpre o propósito (%v): %q", kind, termos, v)
			}
		}
	}
	// A de véspera é a única que pede resposta — é ela que ainda dá tempo de
	// remarcar e liberar o horário.
	for _, v := range textosLembrete[LembreteVespera] {
		if !strings.Contains(v, "?") {
			t.Errorf("lembrete de véspera deveria fazer pergunta: %q", v)
		}
	}
}

func TestTextoDoLembreteSorteia(t *testing.T) {
	vistos := map[string]bool{}
	for i := 0; i < 200; i++ {
		vistos[TextoDoLembrete(LembreteVespera)] = true
	}
	if len(vistos) < 2 {
		t.Errorf("o sorteio não variou: %d texto(s)", len(vistos))
	}
	if TextoDoLembrete("inexistente") != "" {
		t.Error("tipo desconhecido deveria devolver texto vazio, não uma frase qualquer")
	}
}

func TestMensagemUsaONomeDoAluno(t *testing.T) {
	l := LembretePendente{Kind: LembreteVespera, Aluno: "Aula experimental — Guilherme"}
	// O marcador do título não pode vazar para a mensagem do cliente.
	for i := 0; i < 30; i++ {
		m := MensagemDoLembrete(l)
		if strings.Contains(m, "Aula experimental —") {
			t.Fatalf("o marcador interno vazou para o cliente: %q", m)
		}
	}
	// O de uma hora não usa o nome: ele fala de chegada, não da aula.
	umaHora := LembretePendente{Kind: LembreteUmaHora, Aluno: "Aula experimental — Guilherme"}
	if strings.Contains(MensagemDoLembrete(umaHora), "Guilherme") {
		t.Error("o lembrete de 1h não precisa do nome do aluno")
	}
	if MensagemDoLembrete(LembretePendente{Kind: "xpto"}) != "" {
		t.Error("tipo desconhecido deveria devolver vazio")
	}
}

func TestAntecedenciaDosLembretes(t *testing.T) {
	quer := map[TipoLembrete]time.Duration{
		LembreteVespera:     24 * time.Hour,
		LembreteQuatroHoras: 4 * time.Hour,
		LembreteUmaHora:     time.Hour,
	}
	for k, d := range quer {
		if antecedenciaLembrete[k] != d {
			t.Errorf("%s: antecedência %v, queria %v", k, antecedenciaLembrete[k], d)
		}
	}
}
