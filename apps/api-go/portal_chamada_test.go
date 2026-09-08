package main

import "testing"

// O status é o coração do controle de falta: gravar um valor fora da lista
// deixaria o relatório de faltas silenciosamente errado, então a validação
// tem que recusar em vez de aceitar e converter.
func TestPortalStatusChamada(t *testing.T) {
	validos := []string{"presente", "falta", "justificada"}
	for _, v := range validos {
		if !portalStatusChamada[v] {
			t.Errorf("%q devia ser aceito", v)
		}
	}
	invalidos := []string{"", "ausente", "PRESENTE ", "faltou", "p", "presente,falta"}
	for _, v := range invalidos {
		if portalStatusChamada[v] {
			t.Errorf("%q NÃO devia ser aceito", v)
		}
	}
}

// "sem status" e "falta" são coisas diferentes: aula sem chamada feita não
// pode contar como falta do aluno. Este teste documenta a distinção que o
// schema garante (attendance sem linha = não marcado, sem DEFAULT).
func TestAusenciaDeStatusNaoEhFalta(t *testing.T) {
	naoMarcado := ""
	if portalStatusChamada[naoMarcado] {
		t.Fatal("string vazia não pode ser um status válido — seria confundir 'sem chamada' com 'presente'")
	}
	if naoMarcado == "falta" {
		t.Fatal("sem chamada não é falta")
	}
}
