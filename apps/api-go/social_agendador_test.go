package main

import (
	"strings"
	"testing"
	"time"
)

// A janela existe pra um post agendado no passado distante não "acordar" e ir
// ao ar de surpresa quando alguém religar o serviço. Publicação é irreversível.
func TestAgendadorJanelaLimitada(t *testing.T) {
	if agendadorJanela <= 0 {
		t.Fatal("sem janela, post velho publicaria de surpresa")
	}
	if agendadorJanela > 30*24*time.Hour {
		t.Errorf("janela de %v é longa demais pra algo irreversível", agendadorJanela)
	}
}

// Pontualidade: um post marcado pras 15h não pode sair às 21h porque o tique
// é raro.
func TestAgendadorTicaComFrequencia(t *testing.T) {
	if agendadorIntervalo > 5*time.Minute {
		t.Errorf("intervalo de %v atrasa demais um post agendado", agendadorIntervalo)
	}
}

// A trava contra publicação dupla precisa ser DURÁVEL (banco), não em memória
// nem TTL de Redis: restart ou segunda réplica não podem gerar post duplicado,
// e no Instagram não existe desfazer. Este teste guarda a forma da query —
// se alguém trocar o UPDATE condicional por um SELECT+UPDATE, cai aqui.
func TestAgendadorReivindicaAntesDePublicar(t *testing.T) {
	fonte := lerArquivo(t, "social_agendador.go")
	if !strings.Contains(fonte, "auto_published_at = NOW()") ||
		!strings.Contains(fonte, "auto_published_at IS NULL") {
		t.Error("a reivindicação precisa ser um UPDATE condicional em auto_published_at")
	}
	// A marca tem que ser gravada ANTES de publicar, senão a corrida continua.
	iClaim := strings.Index(fonte, "auto_published_at = NOW()")
	iPublish := strings.Index(fonte, "s.runPublish(")
	if iClaim < 0 || iPublish < 0 || iClaim > iPublish {
		t.Error("o post precisa ser reivindicado ANTES de runPublish")
	}
	// Post pulado volta a ficar elegível, senão corrigir o post não adianta.
	if !strings.Contains(fonte, "auto_published_at = NULL") {
		t.Error("post pulado precisa ser liberado pra publicar depois de corrigido")
	}
}
