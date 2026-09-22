package main

import (
	"testing"
	"time"
)

// Os quatro formatos convivem na base hoje, todos digitados à mão. Recusar um
// deles faria o bot deixar de enxergar aulas reais e marcar em cima.
func TestParseIntervaloAceitaOsFormatosDaEscola(t *testing.T) {
	casos := []struct {
		entrada  string
		ini, fim int
	}{
		{"08:00 ~ 10:00", 480, 600},
		{"08:00–09:00", 480, 540},  // travessão
		{"16h00-17h00", 960, 1020}, // h e hífen
		{"13h00-15h00", 780, 900},
		{"19h30-21h30", 1170, 1290},
		{"11:00 ~ 12:00", 660, 720},
		{"8:00 - 9:00", 480, 540},
	}
	for _, c := range casos {
		iv, ok := ParseIntervalo("Quarta", c.entrada)
		if !ok {
			t.Errorf("não entendeu %q — o bot ficaria cego para essa aula", c.entrada)
			continue
		}
		if iv.Inicio != c.ini || iv.Fim != c.fim {
			t.Errorf("%q → %d–%d, queria %d–%d", c.entrada, iv.Inicio, iv.Fim, c.ini, c.fim)
		}
	}
	// Lixo não vira intervalo silenciosamente.
	for _, ruim := range []string{"", "   ", "manhã", "10:00", "25:00-26:00", "10:00-09:00"} {
		if _, ok := ParseIntervalo("Quarta", ruim); ok {
			t.Errorf("%q deveria ser recusado", ruim)
		}
	}
	// Sem dia, não ocupa nada.
	if _, ok := ParseIntervalo("", "08:00 ~ 10:00"); ok {
		t.Error("intervalo sem dia da semana não deveria valer")
	}
}

// O caso real que passou batido: Renata ocupa quarta 08:00–10:00, e o bot
// ofereceu quarta às 8h.
func TestOcupaPegaOCasoDaRenata(t *testing.T) {
	renata, ok := ParseIntervalo("Quarta", "08:00 ~ 10:00")
	if !ok {
		t.Fatal("não parseou")
	}
	quarta := func(h, m int) time.Time {
		return time.Date(2026, 9, 23, h, m, 0, 0, brLocation) // 23/09/2026 é quarta
	}
	if DiaDaSemanaPT(quarta(8, 0)) != "Quarta" {
		t.Fatalf("23/09/2026 deveria ser Quarta, veio %s", DiaDaSemanaPT(quarta(8, 0)))
	}

	casos := []struct {
		nome   string
		quando time.Time
		ocupa  bool
	}{
		{"8h em cima da Renata", quarta(8, 0), true},
		{"9h dentro da aula dela", quarta(9, 0), true},
		{"7h30 encosta no começo", quarta(7, 30), true},
		{"10h logo depois, livre", quarta(10, 0), false},
		{"6h da manhã, livre", quarta(6, 0), false},
	}
	for _, c := range casos {
		if got := renata.Ocupa(c.quando, time.Hour); got != c.ocupa {
			t.Errorf("%s: ocupa=%v, queria %v", c.nome, got, c.ocupa)
		}
	}
	// Outro dia da semana não conflita.
	quinta := time.Date(2026, 9, 24, 8, 0, 0, 0, brLocation)
	if renata.Ocupa(quinta, time.Hour) {
		t.Error("aula de quarta não pode bloquear quinta")
	}
}

// A escola já escreve "Aula Experimental" à mão em quatro linhas. Uma trava
// baseada nesse texto faria a faxina arquivar aulas de verdade.
func TestMarcadorNaoColideComOQueAEscolaEscreve(t *testing.T) {
	delesHoje := []string{
		"Aula Experimental MARTINS",
		"21/09 Aula Experimental",
		"21/09 - Aula Experimental",
		"12/09 Aula Experimental",
		"Renata (só este sábado 19/09)",
		"Turma Informática",
		"Visitar Lumen 3D",
	}
	for _, t2 := range delesHoje {
		if EhDoBot(t2) {
			t.Errorf("%q é da escola — o bot não pode achar que é dele", t2)
		}
	}
	quando := time.Date(2026, 9, 23, 14, 0, 0, 0, brLocation)
	titulo := TituloAulaBot("Caio", quando)
	if !EhDoBot(titulo) {
		t.Errorf("o bot não reconheceu o próprio título: %q", titulo)
	}
	if titulo != "🤖 23/09 Aula experimental — Caio" {
		t.Errorf("título = %q", titulo)
	}
}

// Sem campo de data na base, a data vai no título — é como a escola já faz.
func TestDataNoTitulo(t *testing.T) {
	agora := time.Date(2026, 9, 22, 12, 0, 0, 0, brLocation)
	d, ok := DataNoTitulo("🤖 23/09 Aula experimental — Caio", agora)
	if !ok {
		t.Fatal("não leu a data do próprio título")
	}
	if d.Day() != 23 || d.Month() != time.September || d.Year() != 2026 {
		t.Errorf("data = %v", d)
	}
	// Título da escola não é lido: adivinhar data de texto humano é chute.
	if _, ok := DataNoTitulo("21/09 Aula Experimental", agora); ok {
		t.Error("não deveria ler data de título que não é do bot")
	}
	// Vira do ano: 05/01 visto em dezembro é do ano que vem, não deste.
	dez := time.Date(2026, 12, 20, 12, 0, 0, 0, brLocation)
	if d, ok := DataNoTitulo("🤖 05/01 Aula experimental — X", dez); ok && d.Year() != 2026 {
		t.Errorf("05/01 em dezembro virou %v", d)
	}
}

func TestFormataIntervaloSegueOPadraoDaCasa(t *testing.T) {
	ini := time.Date(2026, 9, 23, 14, 0, 0, 0, brLocation)
	if got := FormataIntervalo(ini, time.Hour); got != "14:00 ~ 15:00" {
		t.Errorf("formato = %q; a base usa '~' em 21 das 33 linhas", got)
	}
}
