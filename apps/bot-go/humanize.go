package main

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// FirstBubbleDelayMs calcula o delay antes do primeiro balão.
// base=800ms, +50ms por palavra, +20ms por caractere, máx 4000ms.
func FirstBubbleDelayMs(text string) time.Duration {
	words := len(strings.Fields(text))
	chars := len(text)
	ms := 800 + words*50 + chars*20
	if ms > 4000 {
		ms = 4000
	}
	return time.Duration(ms) * time.Millisecond
}

// BetweenBubblesDelayMs calcula o delay entre balões consecutivos.
//
// O tempo é o de ESCREVER o balão que vem a seguir, não o de ler o anterior —
// por isso a conta é sobre o texto que está prestes a sair. Uma pessoa digitando
// no celular faz uns 25 a 30 caracteres por segundo quando já sabe o que vai
// dizer; abaixo disso a conversa parece robô respondendo em lote, acima disso
// parece que a pessoa sumiu.
//
// Base de 900ms porque existe o tempo de pensar antes de começar a digitar, e
// teto de 6s porque ninguém espera mais que isso sem achar que a conversa
// travou.
func BetweenBubblesDelayMs(text string) time.Duration {
	ms := 900 + len([]rune(text))*35
	if ms > 6000 {
		ms = 6000
	}
	return time.Duration(ms) * time.Millisecond
}

// DepoisDoAudioDelayMs é a pausa entre a nota de voz e o texto que a segue.
//
// Maior que a pausa entre dois textos de propósito: quem acabou de gravar um
// áudio não começa a digitar no mesmo segundo — ouve o próprio áudio sair,
// respira, e aí escreve. Sem isso, os dois chegam praticamente juntos e o
// conjunto denuncia automação mais do que o áudio gravado ajuda.
//
// Ainda soma o tempo de digitar o texto que vem: um parágrafo longo depois do
// áudio precisa do tempo de ser escrito.
func DepoisDoAudioDelayMs(duracaoAudio time.Duration, proximoTexto string) time.Duration {
	// A pessoa "ouve" o próprio áudio sair antes de digitar — uma fração da
	// duração basta para dar o ritmo, sem fazer o cliente esperar o áudio
	// inteiro de novo.
	ms := 1200 + int(duracaoAudio.Milliseconds())/3 + len([]rune(proximoTexto))*35
	if ms > 9000 {
		ms = 9000
	}
	return time.Duration(ms) * time.Millisecond
}

// QuietHoursHoldMs retorna quantos milissegundos faltam para o término das quiet hours.
// Retorna 0 se now não estiver dentro do período de quiet hours.
// start e end são strings no formato "HH:MM", ex: "22:00", "08:00".
// Suporta períodos que cruzam meia-noite (ex: 22:00–08:00).
func QuietHoursHoldMs(now time.Time, timezone, start, end string) time.Duration {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		// timezone inválido: não aplica quiet hours
		return 0
	}

	local := now.In(loc)

	startH, startM := parseHHMM(start)
	endH, endM := parseHHMM(end)

	startOfDay := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	qStart := startOfDay.Add(time.Duration(startH)*time.Hour + time.Duration(startM)*time.Minute)
	qEnd := startOfDay.Add(time.Duration(endH)*time.Hour + time.Duration(endM)*time.Minute)

	// Período cruza meia-noite (ex: 22:00 → 08:00)
	crossesMidnight := !qEnd.After(qStart)

	var inQuiet bool
	var wakeAt time.Time

	if crossesMidnight {
		// Está em quiet hours se now >= qStart OU now < qEnd
		if !local.Before(qStart) {
			// now está na janela noturna (depois de qStart)
			// wakeAt = qEnd do dia seguinte
			inQuiet = true
			wakeAt = qEnd.Add(24 * time.Hour)
		} else if local.Before(qEnd) {
			// now está na madrugada (antes de qEnd, ainda em quiet hours)
			inQuiet = true
			wakeAt = qEnd
		}
	} else {
		// Período não cruza meia-noite (ex: 13:00 → 17:00)
		if !local.Before(qStart) && local.Before(qEnd) {
			inQuiet = true
			wakeAt = qEnd
		}
	}

	if !inQuiet {
		return 0
	}

	hold := wakeAt.Sub(local)
	if hold < 0 {
		return 0
	}
	return hold
}

// RandBetween retorna uma duração aleatória entre min e max (inclusive).
func RandBetween(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	return min + time.Duration(rand.Int63n(int64(max-min+1)))
}

// parseHHMM converte "HH:MM" em (horas, minutos). Retorna (0,0) em caso de erro.
func parseHHMM(s string) (int, int) {
	var h, m int
	_, err := fmt.Sscanf(s, "%d:%d", &h, &m)
	if err != nil {
		return 0, 0
	}
	return h, m
}
