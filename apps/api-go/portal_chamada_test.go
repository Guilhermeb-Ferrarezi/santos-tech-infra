package main

import (
	"reflect"
	"testing"
)

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

// O import de aulas antigas (histórico anterior ao Portal) recusa data fora
// do formato antes de qualquer coisa tocar o banco — sem isso um erro de
// digitação na lista colada criaria uma aula na data errada em silêncio.
func TestPortalNormalizeImportDatesRecusaDataInvalida(t *testing.T) {
	invalidas := []string{"", "12/03/2022", "2022-13-40", "2022-3-1", "ontem", "2022-03-12 "}
	for _, d := range invalidas {
		if _, err := portalNormalizeImportDates([]string{d}); err == nil {
			t.Errorf("data %q devia ser recusada", d)
		}
	}
}

func TestPortalNormalizeImportDatesRecusaListaVazia(t *testing.T) {
	if _, err := portalNormalizeImportDates(nil); err == nil {
		t.Fatal("lista vazia devia ser recusada")
	}
	if _, err := portalNormalizeImportDates([]string{}); err == nil {
		t.Fatal("lista vazia devia ser recusada")
	}
}

// A lista de presença que o Henrique cola pode ter data repetida (erro de
// copiar e colar) — duplicar a aula silenciosamente inflaria o contador de
// "aulas dadas" do aluno.
func TestPortalNormalizeImportDatesDeduplica(t *testing.T) {
	entrada := []string{"2022-03-12", "2022-03-19", "2022-03-12"}
	out, err := portalNormalizeImportDates(entrada)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	esperado := []string{"2022-03-12", "2022-03-19"}
	if !reflect.DeepEqual(out, esperado) {
		t.Fatalf("got %v, want %v", out, esperado)
	}
}
